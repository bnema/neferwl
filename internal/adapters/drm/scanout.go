package drm

import (
	"errors"
	"os"
	"slices"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
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
// The plane's formats are checked by scanoutFB.
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
	case c.Width != w || c.Height != h:
		return c, "size_mismatch"
	case c.LogicalW != full.Rect.W || c.LogicalH != full.Rect.H:
		return c, "logical_mismatch"
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
	// noAsync: KMS refused an async flip to it; it flips at vblank.
	noAsync bool
	// overlayFailed is why the overlay plane refused it; kept apart from
	// failed, so a refusal on one plane does not bar the other.
	overlayFailed string
	importErr     bool
}

// scanoutFB returns the framebuffer of a client buffer for the primary
// plane, importing it on first use. A buffer KMS refuses is remembered
// with its reason.
func (o *Output) scanoutFB(b *ports.DMABuf, now time.Time) (uint32, string) {
	return o.planeFB(o.primary, b, now, func(fb *clientFB) *string { return &fb.failed })
}

// overlayFB is scanoutFB for the overlay plane, with its own formats and
// refusal reason.
func (o *Output) overlayFB(b *ports.DMABuf, now time.Time) (uint32, string) {
	return o.planeFB(o.overlay, b, now, func(fb *clientFB) *string { return &fb.overlayFailed })
}

// planeFB imports b once as a framebuffer; reason is the plane's refusal
// field. Both planes share the framebuffer: they show the same format.
func (o *Output) planeFB(p *plane, b *ports.DMABuf, now time.Time, reason func(*clientFB) *string) (uint32, string) {
	fb := o.clientFBs[b.ID]
	if fb == nil {
		fb = &clientFB{}
		o.clientFBs[b.ID] = fb
	}
	fb.last = now
	why := reason(fb)
	if *why != "" {
		return 0, *why
	}
	format, ok := planeFormat(p, b)
	if !ok {
		*why = "format"
		return 0, *why
	}
	if fb.fbID == 0 {
		if fb.importErr {
			*why = "import_failed"
			return 0, *why
		}
		id, err := o.k.addFB(b, format)
		if err != nil {
			o.log.Info().Err(err).Uint32("format", b.Format).Uint64("modifier", b.Modifier).Msg("scanout import failed")
			fb.importErr, *why = true, "import_failed"
			return 0, *why
		}
		fb.fbID = id
	}
	return fb.fbID, ""
}

// scanoutFormat is the framebuffer format a client buffer is scanned out
// as on the primary plane (see planeFormat).
func (o *Output) scanoutFormat(b *ports.DMABuf) (uint32, bool) { return planeFormat(o.primary, b) }

// planeFormat is the framebuffer format a client buffer shows as on p,
// when p lists it with the buffer's modifier. Planes here show no alpha:
// an alpha format goes as its X variant when the plane has that one.
func planeFormat(p *plane, b *ports.DMABuf) (uint32, bool) {
	has := func(f uint32) bool {
		return slices.Contains(p.formats, ports.DMABufFormat{Format: f, Modifier: b.Modifier})
	}
	if x, ok := opaqueVariant[b.Format]; ok && has(x) {
		return x, true
	}
	return b.Format, has(b.Format)
}

// opaqueVariant maps alpha formats to the same layout without alpha.
var opaqueVariant = map[uint32]uint32{
	fourccARGB: fourccXRGB,
	fourccABGR: fourccXBGR,
	fourccAR30: fourccXR30,
	fourccAB30: fourccXB30,
	fourccAR4H: fourccXR4H,
	fourccAB4H: fourccXB4H,
}

// scanoutFormats are the formats a buffer can be scanned out with: in the
// primary plane's IN_FORMATS (directly or as its opaque variant) and
// composable by the renderer.
func (o *Output) scanoutFormats(sampled []ports.DMABufFormat) []ports.DMABufFormat {
	var out []ports.DMABufFormat
	for _, f := range sampled {
		b := ports.DMABuf{Format: f.Format, Modifier: f.Modifier}
		if _, ok := o.scanoutFormat(&b); ok {
			out = append(out, f)
		}
	}
	return out
}

// isTenBit identifies the packed 2101010 formats supported by the renderer.
func isTenBit(format uint32) bool {
	switch format {
	case fourccXR30, fourccAR30, fourccXB30, fourccAB30:
		return true
	}
	return false
}

const (
	fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	fourccARGB = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
	fourccXBGR = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	fourccABGR = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
	fourccXR30 = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
	fourccAR30 = 'A' | 'R'<<8 | '3'<<16 | '0'<<24
	fourccXB30 = 'X' | 'B'<<8 | '3'<<16 | '0'<<24
	fourccAB30 = 'A' | 'B'<<8 | '3'<<16 | '0'<<24
	fourccXR4H = 'X' | 'R'<<8 | '4'<<16 | 'H'<<24
	fourccAR4H = 'A' | 'R'<<8 | '4'<<16 | 'H'<<24
	fourccXB4H = 'X' | 'B'<<8 | '4'<<16 | 'H'<<24
	fourccAB4H = 'A' | 'B'<<8 | '4'<<16 | 'H'<<24
)

// addFB imports a dmabuf as a framebuffer of the given fourcc: client buffers, the renderer's
// output images and cursor images. The GEM handle belongs to the card fd
// and is shared by every import of the same buffer, so import, ADDFB2 and
// close run under the lock and the handle is closed right away: the
// framebuffer keeps its own reference to the buffer.
func (k kmsDevice) addFB(b *ports.DMABuf, format uint32) (uint32, error) {
	raw, err := b.Planes[0].File.SyscallConn()
	if err != nil {
		return 0, err
	}
	k.gemMu.Lock()
	defer k.gemMu.Unlock()
	var p primeHandle
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		p.fd = int32(fd)
		ioErr = ioctl(k.fd, ioctlPrimeFDToHandle, unsafe.Pointer(&p))
	}); err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, errors.Join(errors.New("prime import"), ioErr)
	}
	defer func() {
		g := gemClose{handle: p.handle}
		_ = ioctl(k.fd, ioctlGemClose, unsafe.Pointer(&g))
	}()
	cmd := fbCmd2{width: uint32(b.Width), height: uint32(b.Height), format: format}
	useMod := b.Modifier != modInvalid && (b.Modifier != 0 || k.modifiers)
	if useMod {
		cmd.flags = fbModifiers
	}
	// Planes in the same dmabuf share its GEM handle: each distinct
	// handle is closed once.
	closing := map[uint32]bool{p.handle: true}
	for i, pl := range b.Planes {
		if i >= len(cmd.handles) {
			break
		}
		h := p.handle
		if i > 0 {
			var err error
			if h, err = k.primeImport(pl.File); err != nil {
				return 0, err
			}
			if !closing[h] {
				closing[h] = true
				defer k.gemClose(h)
			}
		}
		cmd.handles[i], cmd.pitches[i], cmd.offsets[i] = h, pl.Stride, pl.Offset
		if useMod {
			cmd.modifiers[i] = b.Modifier
		}
	}
	if err := ioctl(k.fd, ioctlAddFB2, unsafe.Pointer(&cmd)); err != nil {
		return 0, errors.Join(errors.New("add fb2"), err)
	}
	return cmd.fbID, nil
}

// primeImport returns the GEM handle of a dmabuf file. The caller holds
// gemMu and closes the handle.
func (k kmsDevice) primeImport(f *os.File) (uint32, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var p primeHandle
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		p.fd = int32(fd)
		ioErr = ioctl(k.fd, ioctlPrimeFDToHandle, unsafe.Pointer(&p))
	}); err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, errors.Join(errors.New("prime import"), ioErr)
	}
	return p.handle, nil
}

func (k kmsDevice) gemClose(h uint32) {
	g := gemClose{handle: h}
	_ = ioctl(k.fd, ioctlGemClose, unsafe.Pointer(&g))
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
			_ = o.k.rmFB(fb.fbID)
		}
		delete(o.clientFBs, id)
	}
}

// dupFence duplicates a sync file under its SyscallConn, so a concurrent
// Close cannot hand us a reused fd; nil when f is nil or closed.
func dupFence(f *os.File) *os.File {
	if f == nil {
		return nil
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return nil
	}
	fd := -1
	if raw.Control(func(v uintptr) { fd, _ = unix.FcntlInt(v, unix.F_DUPFD_CLOEXEC, 0) }) != nil || fd < 0 {
		return nil
	}
	return os.NewFile(uintptr(fd), "acquire-fence")
}
