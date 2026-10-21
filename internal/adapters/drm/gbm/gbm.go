// Package gbm allocates dmabuf images with libgbm, loaded at runtime with
// purego. Display drivers know best how to lay out a buffer they will scan
// out (NVIDIA's block-linear layouts in particular), so the DRM output asks
// GBM for its images and the renderer imports them.
package gbm

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego"
	"golang.org/x/sys/unix"
)

// ErrUnavailable marks a missing or unusable libgbm.
var ErrUnavailable = errors.New("gbm unavailable")

// Usage is a set of GBM_BO_USE_* flags.
type Usage uint32

const (
	// UseScanout (GBM_BO_USE_SCANOUT): the buffer can be shown by KMS.
	UseScanout Usage = 1 << 0
	// UseRendering (GBM_BO_USE_RENDERING): a GPU can render into it.
	UseRendering Usage = 1 << 2
	// UseLinear (GBM_BO_USE_LINEAR): the layout is linear.
	UseLinear Usage = 1 << 4
)

const (
	// modLinear is DRM_FORMAT_MOD_LINEAR.
	modLinear = 0
	// modInvalid is DRM_FORMAT_MOD_INVALID: an implicit layout.
	modInvalid = 1<<56 - 1
	// maxPlanes is the DRM limit of planes in a framebuffer.
	maxPlanes = 4
)

var (
	loadOnce sync.Once
	loadErr  error

	createDevice         func(fd int32) uintptr
	destroyDevice        func(dev uintptr)
	createWithModifiers2 func(dev uintptr, w, h, format uint32, mods unsafe.Pointer, count uint32, flags uint32) uintptr
	createWithModifiers  func(dev uintptr, w, h, format uint32, mods unsafe.Pointer, count uint32) uintptr
	createPlain          func(dev uintptr, w, h, format uint32, flags uint32) uintptr
	boModifier           func(bo uintptr) uint64
	boPlaneCount         func(bo uintptr) int32
	boFDForPlane         func(bo uintptr, plane int32) int32
	boFD                 func(bo uintptr) int32
	boStrideForPlane     func(bo uintptr, plane int32) uint32
	boOffset             func(bo uintptr, plane int32) uint32
	boDestroy            func(bo uintptr)
)

// load opens libgbm once. Without it (or with a lacking one) the allocator
// is unavailable and callers fall back to another way to make images.
func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen("libgbm.so.1", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			loadErr = fmt.Errorf("%w: open libgbm.so.1: %v", ErrUnavailable, err)
			return
		}
		defer func() {
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("%w: libgbm: %v", ErrUnavailable, r)
			}
		}()
		reg := func(fn any, name string) { purego.RegisterLibFunc(fn, lib, name) }
		have := func(name string) bool { p, err := purego.Dlsym(lib, name); return err == nil && p != 0 }
		reg(&createDevice, "gbm_create_device")
		reg(&destroyDevice, "gbm_device_destroy")
		reg(&createPlain, "gbm_bo_create")
		reg(&boModifier, "gbm_bo_get_modifier")
		reg(&boPlaneCount, "gbm_bo_get_plane_count")
		if have("gbm_bo_get_fd_for_plane") {
			reg(&boFDForPlane, "gbm_bo_get_fd_for_plane")
		} else {
			// Older libgbm: one fd for the whole object.
			reg(&boFD, "gbm_bo_get_fd")
		}
		reg(&boStrideForPlane, "gbm_bo_get_stride_for_plane")
		reg(&boOffset, "gbm_bo_get_offset")
		reg(&boDestroy, "gbm_bo_destroy")
		if have("gbm_bo_create_with_modifiers2") {
			reg(&createWithModifiers2, "gbm_bo_create_with_modifiers2")
		}
		if have("gbm_bo_create_with_modifiers") {
			reg(&createWithModifiers, "gbm_bo_create_with_modifiers")
		}
		if createWithModifiers2 == nil && createWithModifiers == nil {
			loadErr = fmt.Errorf("%w: libgbm lacks gbm_bo_create_with_modifiers", ErrUnavailable)
		}
	})
	return loadErr
}

// Device is a GBM device on a duplicate of a DRM device fd. Close its
// buffers before the device.
type Device struct {
	dev uintptr
	fd  int
}

// Open creates a GBM device on a duplicate of fd, which stays the caller's.
func Open(fd int) (*Device, error) {
	if err := load(); err != nil {
		return nil, err
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate drm fd: %w", err)
	}
	dev := createDevice(int32(dup))
	if dev == 0 {
		_ = unix.Close(dup)
		return nil, errors.New("gbm_create_device failed")
	}
	return &Device{dev: dev, fd: dup}, nil
}

// Close destroys the device and its fd.
func (d *Device) Close() {
	if d == nil || d.dev == 0 {
		return
	}
	destroyDevice(d.dev)
	_ = unix.Close(d.fd)
	d.dev = 0
}

// Buffer is an allocated image. It owns one fd per plane and the GBM
// object, kept alive for as long as the image is used.
type Buffer struct {
	bo            uintptr
	Width, Height int
	Format        uint32
	Modifier      uint64
	// Flagless: the buffer came from gbm_bo_create_with_modifiers, which
	// takes no usage flags (libgbm before 21.1): scanout was not asked for.
	Flagless bool
	planes   []plane
}

type plane struct {
	fd             int
	offset, stride uint32
}

// Create allocates an image whose modifier is one of modifiers (a
// non-empty list). A list holding only linear falls back to a plain
// linear allocation when GBM refuses the modifier call, and accepts an
// implicit layout (DRM_FORMAT_MOD_INVALID) as linear on either path. The
// result must be one memory object, because the renderer imports one
// dmabuf for all planes.
func (d *Device) Create(width, height int, format uint32, modifiers []uint64, usage Usage) (*Buffer, error) {
	if d == nil || d.dev == 0 {
		return nil, errors.New("gbm device closed")
	}
	if width <= 0 || height <= 0 || len(modifiers) == 0 {
		return nil, fmt.Errorf("gbm: bad request %dx%d with %d modifiers", width, height, len(modifiers))
	}
	var bo uintptr
	flagless := false
	switch {
	case createWithModifiers2 != nil:
		bo = createWithModifiers2(d.dev, uint32(width), uint32(height), format, unsafe.Pointer(&modifiers[0]), uint32(len(modifiers)), uint32(usage))
	case createWithModifiers != nil:
		// This entry point cannot ask for scanout.
		flagless = true
		bo = createWithModifiers(d.dev, uint32(width), uint32(height), format, unsafe.Pointer(&modifiers[0]), uint32(len(modifiers)))
	}
	runtime.KeepAlive(modifiers)
	onlyLinear := len(modifiers) == 1 && modifiers[0] == modLinear
	if bo == 0 && onlyLinear {
		bo = createPlain(d.dev, uint32(width), uint32(height), format, uint32(usage|UseLinear))
		flagless = false
	}
	if bo == 0 {
		return nil, fmt.Errorf("gbm: no %dx%d buffer of format %#x with modifiers %#x", width, height, format, modifiers)
	}
	b, err := readBuffer(bo, width, height, format, acceptedModifiers(modifiers))
	if err != nil {
		boDestroy(bo)
		return nil, err
	}
	if onlyLinear && b.Modifier == modInvalid {
		b.Modifier = modLinear
	}
	b.Flagless = flagless
	return b, nil
}

// acceptedModifiers is what an allocation may report for a request: the
// request itself, plus an implicit layout when only linear was asked for.
func acceptedModifiers(requested []uint64) []uint64 {
	if len(requested) == 1 && requested[0] == modLinear {
		return []uint64{modLinear, modInvalid}
	}
	return requested
}

// readBuffer reads the layout of bo and validates it against the request.
// It does not destroy bo on error.
func readBuffer(bo uintptr, width, height int, format uint32, modifiers []uint64) (*Buffer, error) {
	b := &Buffer{bo: bo, Width: width, Height: height, Format: format, Modifier: boModifier(bo)}
	if err := checkModifier(modifiers, b.Modifier); err != nil {
		return nil, err
	}
	n := int(boPlaneCount(bo))
	if n < 1 || n > maxPlanes {
		return nil, fmt.Errorf("gbm: buffer has %d planes", n)
	}
	for i := range n {
		var fd int32
		if boFDForPlane != nil {
			fd = boFDForPlane(bo, int32(i))
		} else {
			fd = boFD(bo)
		}
		if fd < 0 {
			b.closeFDs()
			return nil, fmt.Errorf("gbm: no fd for plane %d", i)
		}
		b.planes = append(b.planes, plane{fd: int(fd), offset: boOffset(bo, int32(i)), stride: boStrideForPlane(bo, int32(i))})
	}
	fds := make([]int, len(b.planes))
	for i, p := range b.planes {
		fds[i] = p.fd
	}
	if err := checkOneObject(fds); err != nil {
		b.closeFDs()
		return nil, err
	}
	b.bo = bo
	return b, nil
}

// checkModifier fails when the allocator returned a modifier that was not
// asked for (including DRM_FORMAT_MOD_INVALID, an implicit layout the
// renderer cannot import).
func checkModifier(requested []uint64, got uint64) error {
	if !slices.Contains(requested, got) {
		return fmt.Errorf("gbm: modifier %#x not in the requested list %#x", got, requested)
	}
	return nil
}

// checkOneObject fails when the plane fds are not all the same dmabuf:
// disjoint planes cannot be imported as one memory object.
func checkOneObject(fds []int) error {
	var first unix.Stat_t
	for i, fd := range fds {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return fmt.Errorf("gbm: stat plane %d: %w", i, err)
		}
		if i == 0 {
			first = st
			continue
		}
		if st.Dev != first.Dev || st.Ino != first.Ino {
			return fmt.Errorf("gbm: plane %d is a different dmabuf than plane 0", i)
		}
	}
	return nil
}

func (b *Buffer) closeFDs() {
	for _, p := range b.planes {
		_ = unix.Close(p.fd)
	}
	b.planes = nil
}

// DMABuf returns the image as a dmabuf with fresh descriptors (the caller
// closes them); the buffer keeps its own.
func (b *Buffer) DMABuf() (ports.DMABuf, error) {
	if b == nil || b.bo == 0 {
		return ports.DMABuf{}, errors.New("gbm buffer closed")
	}
	out := ports.DMABuf{Width: b.Width, Height: b.Height, Format: b.Format, Modifier: b.Modifier}
	for _, p := range b.planes {
		fd, err := unix.FcntlInt(uintptr(p.fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			for _, q := range out.Planes {
				_ = q.File.Close()
			}
			return ports.DMABuf{}, fmt.Errorf("duplicate gbm plane fd: %w", err)
		}
		out.Planes = append(out.Planes, ports.DMABufPlane{File: os.NewFile(uintptr(fd), "neferwl-gbm"), Offset: p.offset, Stride: p.stride})
	}
	return out, nil
}

// Close frees the buffer and its descriptors.
func (b *Buffer) Close() {
	if b == nil || b.bo == 0 {
		return
	}
	b.closeFDs()
	boDestroy(b.bo)
	b.bo = 0
}
