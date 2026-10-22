package drm

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// KMS dumb buffers: the driver allocates a linear buffer the CPU maps and
// the display scans out. Cursor images use them (see cursor_images.go).

const (
	ioctlCreateDumb  = 0xC02064B2 // DRM_IOWR('d', 0xB2, struct drm_mode_create_dumb)
	ioctlMapDumb     = 0xC01064B3 // DRM_IOWR('d', 0xB3, struct drm_mode_map_dumb)
	ioctlDestroyDumb = 0xC00464B4 // DRM_IOWR('d', 0xB4, struct drm_mode_destroy_dumb)
)

type createDumb struct {
	height, width, bpp, flags uint32
	handle, pitch             uint32
	size                      uint64
}

type mapDumb struct {
	handle, pad uint32
	offset      uint64
}

// dumbBuffer is a dumb buffer's GEM handle, row pitch and size in bytes.
type dumbBuffer struct {
	handle, pitch uint32
	size          uint64
}

func (k kmsDevice) createDumb(width, height int) (dumbBuffer, error) {
	if width <= 0 || height <= 0 {
		return dumbBuffer{}, fmt.Errorf("dumb buffer size %dx%d", width, height)
	}
	c := createDumb{height: uint32(height), width: uint32(width), bpp: 32}
	if err := ioctl(k.fd, ioctlCreateDumb, unsafe.Pointer(&c)); err != nil {
		return dumbBuffer{}, err
	}
	return dumbBuffer{handle: c.handle, pitch: c.pitch, size: c.size}, nil
}

func (k kmsDevice) mapDumb(b dumbBuffer) ([]byte, error) {
	m := mapDumb{handle: b.handle}
	if err := ioctl(k.fd, ioctlMapDumb, unsafe.Pointer(&m)); err != nil {
		return nil, err
	}
	if b.size == 0 || b.size > 1<<30 {
		return nil, fmt.Errorf("dumb buffer size %d", b.size)
	}
	return unix.Mmap(k.fd, int64(m.offset), int(b.size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
}

func (k kmsDevice) unmapDumb(mem []byte) error { return unix.Munmap(mem) }

// addFBHandle makes a framebuffer of format over the dumb buffer, without
// modifier (the driver's linear layout).
func (k kmsDevice) addFBHandle(b dumbBuffer, width, height int, format uint32) (uint32, error) {
	if b.handle == 0 {
		return 0, errors.New("no dumb buffer")
	}
	cmd := fbCmd2{width: uint32(width), height: uint32(height), format: format}
	cmd.handles[0], cmd.pitches[0] = b.handle, b.pitch
	if err := ioctl(k.fd, ioctlAddFB2, unsafe.Pointer(&cmd)); err != nil {
		return 0, errors.Join(errors.New("add fb2"), err)
	}
	return cmd.fbID, nil
}

func (k kmsDevice) destroyDumb(handle uint32) error {
	return ioctl(k.fd, ioctlDestroyDumb, unsafe.Pointer(&handle))
}
