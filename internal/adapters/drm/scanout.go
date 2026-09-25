package drm

import (
	"errors"
	"time"
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

	fbModifiers        = 1 << 1
	capAddFB2Modifiers = 0x10
	modInvalid         = 0x00ffffffffffffff
	scanoutIdleTTL     = 5 * time.Second // a cached framebuffer survives unused
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
	case c.LogicalW != full.Rect.W || c.LogicalH != full.Rect.H:
		return c, "logical_mismatch"
	case !scanoutable(c.DMABuf.Format):
		return c, "format"
	case c.Geometry != (ports.Rect{}) && (c.Geometry.X != 0 || c.Geometry.Y != 0 || c.Geometry.W != c.LogicalW || c.Geometry.H != c.LogicalH):
		return c, "geometry_crop"
	}
	return c, ""
}

// clientFB is a client buffer imported as a KMS framebuffer.
type clientFB struct {
	fbID uint32
	last time.Time // when last wanted on screen
	// failed is the reason KMS refused the buffer; it is not retried.
	failed string
}

// scanoutFB returns the framebuffer of a client buffer, importing it on
// first use. A buffer KMS refuses is remembered with its reason.
func (o *Output) scanoutFB(b *ports.DMABuf, now time.Time) (uint32, string) {
	if fb := o.clientFBs[b.ID]; fb != nil {
		fb.last = now
		return fb.fbID, fb.failed
	}
	fb := &clientFB{last: now}
	o.clientFBs[b.ID] = fb
	id, err := o.card.addFB(b)
	if err != nil {
		o.log.Info().Err(err).Uint32("format", b.Format).Uint64("modifier", b.Modifier).Msg("scanout import failed")
		fb.failed = "import_failed"
		return 0, fb.failed
	}
	fb.fbID = id
	return id, ""
}

// scanoutFormat is the framebuffer format for a client format. The primary
// plane shows no alpha, and a legacy page flip keeps the format of the
// composed image (XRGB8888): alpha formats are scanned out as their X
// variant, others are refused.
func scanoutFormat(f uint32) (uint32, bool) {
	switch f {
	case fourccXRGB, fourccARGB:
		return fourccXRGB, true
	}
	return 0, false
}

const (
	fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	fourccARGB = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
)

// addFB imports a dmabuf as a framebuffer: client buffers and the
// renderer's exported output images. The GEM handle belongs to the card fd
// and is shared by every import of the same buffer, so import, ADDFB2 and
// close run under the card lock and the handle is closed right away: the
// framebuffer keeps its own reference to the buffer.
func (c *Card) addFB(b *ports.DMABuf) (uint32, error) {
	format, ok := scanoutFormat(b.Format)
	if !ok {
		return 0, errors.New("format not scanned out")
	}
	raw, err := b.Planes[0].File.SyscallConn()
	if err != nil {
		return 0, err
	}
	c.gemMu.Lock()
	defer c.gemMu.Unlock()
	var p primeHandle
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		p.fd = int32(fd)
		ioErr = ioctl(c.fd, ioctlPrimeFDToHandle, unsafe.Pointer(&p))
	}); err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, errors.Join(errors.New("prime import"), ioErr)
	}
	defer func() {
		g := gemClose{handle: p.handle}
		_ = ioctl(c.fd, ioctlGemClose, unsafe.Pointer(&g))
	}()
	cmd := fbCmd2{width: uint32(b.Width), height: uint32(b.Height), format: format}
	cmd.handles[0], cmd.pitches[0], cmd.offsets[0] = p.handle, b.Planes[0].Stride, b.Planes[0].Offset
	if b.Modifier != modInvalid && (b.Modifier != 0 || c.modifiers) {
		cmd.flags, cmd.modifiers[0] = fbModifiers, b.Modifier
	}
	if err := ioctl(c.fd, ioctlAddFB2, unsafe.Pointer(&cmd)); err != nil {
		return 0, errors.Join(errors.New("add fb2"), err)
	}
	return cmd.fbID, nil
}

// dropClientFBs frees framebuffers not wanted for scanoutIdleTTL, or all
// of them, except those on screen or queued.
func (o *Output) dropClientFBs(now time.Time, all bool) {
	for id, fb := range o.clientFBs {
		if id == o.shown || id == o.queued {
			continue
		}
		if !all && now.Sub(fb.last) < scanoutIdleTTL {
			continue
		}
		if fb.fbID != 0 {
			v := fb.fbID
			_ = ioctl(o.fd, ioctlRmFB, unsafe.Pointer(&v))
		}
		delete(o.clientFBs, id)
	}
}

func scanoutable(f uint32) bool { _, ok := scanoutFormat(f); return ok }
