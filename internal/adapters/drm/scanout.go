package drm

import (
	"errors"
	"os"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
)

// Direct scanout (ADR 006): when one fullscreen window covers the output
// with a GPU buffer of the output's size, the output flips to that buffer
// instead of compositing it. The buffer becomes a KMS framebuffer once
// (PRIME import + ADDFB2) and is cached by DMABuf ID while the client
// keeps using it. KMS waits on the buffer's implicit fences before showing
// it. Anything else on screen, a scale, or a buffer KMS refuses falls back
// to composition.

const (
	ioctlGemClose        = 0x40086409
	ioctlPrimeFDToHandle = 0xC00C642E
	ioctlAddFB2          = 0xC06864B8

	fbModifiers    = 1 << 1
	modInvalid     = 0x00ffffffffffffff
	scanoutIdleTTL = 240 // flips a cached framebuffer survives unused
)

type primeHandle struct {
	handle, flags uint32
	fd            int32
}

type gemClose struct{ handle, pad uint32 }

type fbCmd2 struct {
	fbID, width, height, format, flags uint32
	handles, pitches, offsets          [4]uint32
	modifiers                          [4]uint64
}

// scanoutCandidate returns the window content the output can scan out
// directly, or a reason why it cannot. w, h are the output's physical size.
func scanoutCandidate(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, w, h int) (ports.SurfaceContent, string) {
	var full *ports.SceneWindow
	for i := range s.Windows {
		win := &s.Windows[i]
		if win.Hidden || win.Rect.W <= 0 || win.Rect.H <= 0 {
			continue
		}
		covers := win.Rect.X == 0 && win.Rect.Y == 0 && win.Rect.W == s.OutputWidth && win.Rect.H == s.OutputHeight
		if !win.Fullscreen || !covers || full != nil {
			return ports.SurfaceContent{}, "other_windows"
		}
		full = win
	}
	if full == nil {
		return ports.SurfaceContent{}, "no_fullscreen"
	}
	for _, l := range s.Layers {
		if l.Layer == ports.LayerOverlay && l.Rect.W > 0 && l.Rect.H > 0 {
			return ports.SurfaceContent{}, "overlay_surface"
		}
	}
	c := surfaces[full.ID]
	switch {
	case c.DMABuf == nil:
		return c, "not_dmabuf"
	case len(c.Children) > 0:
		return c, "subsurfaces"
	case len(c.DMABuf.Planes) != 1:
		return c, "multi_plane"
	case c.Width != w || c.Height != h:
		return c, "size_mismatch"
	case c.Geometry != (ports.Rect{}) && (c.Geometry.X != 0 || c.Geometry.Y != 0 || c.Geometry.W != c.LogicalW || c.Geometry.H != c.LogicalH):
		return c, "geometry_crop"
	}
	return c, ""
}

// clientFB is a client buffer imported as a KMS framebuffer.
type clientFB struct {
	fbID, handle uint32
	last         int // flip count when last shown
	failed       bool
}

// scanoutFB returns the framebuffer of a client buffer, importing it on
// first use. A buffer KMS refuses is remembered and never retried.
func (o *Output) scanoutFB(b *ports.DMABuf) (uint32, error) {
	if fb := o.clientFBs[b.ID]; fb != nil {
		fb.last = o.flips
		if fb.failed {
			return 0, errScanoutRefused
		}
		return fb.fbID, nil
	}
	fb := &clientFB{last: o.flips}
	o.clientFBs[b.ID] = fb
	handle, err := o.importHandle(b.Planes[0].File)
	if err != nil {
		fb.failed = true
		return 0, errors.Join(errors.New("prime import"), err)
	}
	cmd := fbCmd2{width: uint32(b.Width), height: uint32(b.Height), format: b.Format}
	cmd.handles[0], cmd.pitches[0], cmd.offsets[0] = handle, b.Planes[0].Stride, b.Planes[0].Offset
	if b.Modifier != modInvalid {
		cmd.flags, cmd.modifiers[0] = fbModifiers, b.Modifier
	}
	if err := ioctl(o.fd, ioctlAddFB2, unsafe.Pointer(&cmd)); err != nil {
		o.closeHandle(handle)
		fb.failed = true
		return 0, errors.Join(errors.New("add fb2"), err)
	}
	fb.fbID, fb.handle = cmd.fbID, handle
	return fb.fbID, nil
}

var errScanoutRefused = errors.New("buffer refused for scanout")

// importHandle turns a dmabuf into a GEM handle on the card. The kernel
// returns the same handle for the same buffer object: handles are counted
// and closed with their last framebuffer.
func (o *Output) importHandle(f *os.File) (uint32, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var p primeHandle
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		p.fd = int32(fd)
		ioErr = ioctl(o.fd, ioctlPrimeFDToHandle, unsafe.Pointer(&p))
	}); err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, ioErr
	}
	o.handles[p.handle]++
	return p.handle, nil
}

func (o *Output) closeHandle(handle uint32) {
	if o.handles[handle]--; o.handles[handle] > 0 {
		return
	}
	delete(o.handles, handle)
	c := gemClose{handle: handle}
	_ = ioctl(o.fd, ioctlGemClose, unsafe.Pointer(&c))
}

// dropClientFBs frees framebuffers unused for scanoutIdleTTL flips, or all
// of them, except those on screen or queued.
func (o *Output) dropClientFBs(all bool) {
	for id, fb := range o.clientFBs {
		if id == o.shown || id == o.queued {
			continue
		}
		if !all && o.flips-fb.last < scanoutIdleTTL {
			continue
		}
		o.freeFB(fb)
		delete(o.clientFBs, id)
	}
}

func (o *Output) freeFB(fb *clientFB) {
	if fb.fbID != 0 {
		id := fb.fbID
		_ = ioctl(o.fd, ioctlRmFB, unsafe.Pointer(&id))
	}
	if fb.handle != 0 {
		o.closeHandle(fb.handle)
	}
}
