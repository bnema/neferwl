package drm

import (
	"errors"
	"math"
	"os"
	"slices"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Direct scanout (ADR 006): when one fullscreen window covers the output
// with a GPU buffer whose source can fill the plane, the output flips to that buffer
// instead of compositing it. The buffer becomes a KMS framebuffer once
// (PRIME import + ADDFB2) and is cached by DMABuf ID while the client
// keeps using it. KMS waits on the buffer's implicit fences before showing
// it. Anything else on screen or a buffer KMS refuses falls back to composition.

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

// planeRect contains source coordinates in 16.16 and destination pixels.
type planeRect struct {
	src  [4]uint64
	crtc [4]uint64
}

func fullPlaneRect(w, h int) planeRect {
	return planeRect{src: [4]uint64{0, 0, uint64(w) << 16, uint64(h) << 16}, crtc: [4]uint64{0, 0, uint64(w), uint64(h)}}
}

func scanoutRect(c ports.SurfaceContent, w, h int) planeRect {
	r := fullPlaneRect(w, h)
	r.src = [4]uint64{0, 0, uint64(c.Width) << 16, uint64(c.Height) << 16}
	if c.Source[2] != 0 {
		for i, value := range c.Source {
			r.src[i] = uint64(float64(value) * 65536)
		}
	}
	return r
}

// covers reports whether win fills the output of scene s.
func covers(s *ports.Scene, win *ports.SceneWindow) bool {
	return (s.WorkspaceClip == (ports.Rect{}) || s.WorkspaceClip == (ports.Rect{W: s.OutputWidth, H: s.OutputHeight})) &&
		win.Rect.X == 0 && win.Rect.Y == 0 && win.Rect.W == s.OutputWidth && win.Rect.H == s.OutputHeight
}

// fullscreenShown reports whether a visible fullscreen window covers the
// output, whether its frame is scanned out or composed.
func fullscreenShown(s *ports.Scene) bool {
	for i := range s.Windows {
		win := &s.Windows[i]
		if win.Fullscreen && !win.Hidden && covers(s, win) {
			return true
		}
	}
	return false
}

// scanoutCandidate returns the window content the output can scan out
// directly, or a reason why it cannot. w, h are the output's physical size.
// The plane's formats are checked by scanoutFB.
func scanoutCandidate(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, w, h int) (ports.SurfaceContent, string) {
	if s.Transform != 0 {
		// The scanout buffer would have to be pre-rotated: rotated outputs compose.
		return ports.SurfaceContent{}, "output_transform"
	}
	if s.WorkspaceClip != (ports.Rect{}) && s.WorkspaceClip != (ports.Rect{W: s.OutputWidth, H: s.OutputHeight}) {
		return ports.SurfaceContent{}, "workspace_clip"
	}
	var full *ports.SceneWindow
	for i := range s.Windows {
		win := &s.Windows[i]
		if win.Hidden || win.Rect.W <= 0 || win.Rect.H <= 0 {
			continue
		}
		if !win.Fullscreen || !covers(&s, win) || full != nil {
			return ports.SurfaceContent{}, "other_windows"
		}
		full = win
	}
	if full == nil {
		return ports.SurfaceContent{}, "no_fullscreen"
	}
	if len(s.DropHints) > 0 {
		// Drag hints are drawn over the window.
		return ports.SurfaceContent{}, "drop_hint"
	}
	if full.Dim > 0 {
		// Its veil is composed over it: the plane would show it bright.
		return ports.SurfaceContent{}, "dim"
	}
	// Core leaves the layers a fullscreen window hides out of the scene:
	// one above the windows here is drawn.
	for _, l := range s.Layers {
		if l.Layer >= ports.LayerTop && l.Rect.W > 0 && l.Rect.H > 0 {
			return ports.SurfaceContent{}, "overlay_surface"
		}
	}
	c := surfaces[full.ID]
	if c.DMABuf == nil && len(c.Children) > 0 {
		var reason string
		if c, reason = clientSubsurface(c); reason != "" {
			return c, reason
		}
	}
	switch {
	case c.DMABuf == nil:
		return c, "not_dmabuf"
	case c.Fade > 0:
		// The plane would show it unfaded (wp_alpha_modifier_v1).
		return c, "fade"
	case len(c.Children) > 0:
		return c, "subsurfaces"
	case c.Transform != 0:
		// Planes show buffers as they are: rotation is composed.
		return c, "buffer_transform"
	case c.LogicalW != full.Rect.W || c.LogicalH != full.Rect.H:
		return c, "letterbox"
	case c.Width <= 0 || c.Height <= 0 || c.Source[2] < 0 || c.Source[3] < 0 || !finiteSource(c.Source):
		return c, "source_crop"
	case c.Geometry != (ports.Rect{}) && (c.Geometry.X != 0 || c.Geometry.Y != 0 || c.Geometry.W != c.LogicalW || c.Geometry.H != c.LogicalH):
		return c, "geometry_crop"
	}
	if c.Source[2] != 0 && (c.Source[0] < 0 || c.Source[1] < 0 || c.Source[2] <= 0 || c.Source[3] <= 0 || c.Source[0]+c.Source[2] > float32(c.Width) || c.Source[1]+c.Source[3] > float32(c.Height)) {
		return c, "source_crop"
	}
	return c, ""
}

func finiteSource(src [4]float32) bool {
	for _, n := range src {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return false
		}
	}
	return true
}

// cropped reports whether a viewport source shows less than the buffer.
func cropped(c ports.SurfaceContent) bool {
	return c.Source[2] != 0 && c.Source != [4]float32{0, 0, float32(c.Width), float32(c.Height)}
}

// clientSubsurface returns the content to scan out for a window whose
// root has no GPU buffer but one opaque subsurface over its geometry: Wine
// draws the frame in the root (wl_shm) and presents Vulkan in a subsurface
// on the client area. That subsurface hides the root entirely, so it is
// what the screen shows. The returned content keeps the window's ID and
// Seq for release and presentation.
func clientSubsurface(c ports.SurfaceContent) (ports.SurfaceContent, string) {
	if len(c.Children) != 1 {
		return c, "subsurfaces"
	}
	ch := c.Children[0]
	switch {
	case ch.DMABuf == nil:
		return c, "not_dmabuf"
	case !ch.Opaque:
		return c, "subsurface_translucent"
	case ch.Below:
		// The root is drawn over it.
		return c, "subsurface_below"
	case ch.X != c.Geometry.X || ch.Y != c.Geometry.Y:
		return c, "subsurface_offset"
	}
	out := ch.SurfaceContent
	out.ID, out.Seq, out.Geometry = c.ID, c.Seq, ports.Rect{}
	return out, ""
}

// scanoutFrame picks a fullscreen client buffer to flip directly. It
// returns fb 0, with the reason logged on change, when the frame must be
// composed.
func (o *Output) scanoutFrame(scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) (fb uint32, c ports.SurfaceContent, mode colorMode) {
	reason := "disabled"
	if o.scanout {
		c, reason = scanoutCandidate(scene, surfaces, o.Width(), o.Height())
		if reason == "" {
			mode, reason = planeColor(c.Color, c.DMABuf.Format, c.Opaque, o.hdr.on, o.primary.pipeline)
		}
	}
	if reason == "" {
		fb, reason = o.scanoutFB(c.DMABuf, time.Now())
		if reason == "" {
			rect := scanoutRect(c, o.Width(), o.Height())
			cfb := o.clientFBs[c.DMABuf.ID]
			if cfb.scaleRefused && cfb.scaleFailed == rect {
				reason = "scale_refused"
			} else if rect != fullPlaneRect(o.Width(), o.Height()) && (!cfb.scaleTestedOK || cfb.scaleTested != rect) {
				req := &o.probeReq
				req.reset()
				o.primaryRectProps(req, fb, rect)
				if err := o.k.commit(req, atomicTestOnly, 0); err != nil {
					cfb.scaleFailed = rect
					cfb.scaleRefused = true
					reason = "scale_refused"
					o.log.Info().Str("component", "render").Err(err).Str("reason", reason).Str("connector", o.conn.name).Msg("scanout scaling refused")
				} else {
					cfb.scaleTested = rect
					cfb.scaleTestedOK = true
				}
			}
			if reason == "" && mode != colorBypass && !o.colorAllowed(o.primary, c.DMABuf.Format, func(req *atomicReq) {
				o.primaryRectProps(req, fb, rect)
				if o.overlayOn != 0 {
					o.overlayProps(req, overlayWin{})
				}
			}) {
				reason = "color_refused"
			}
		}
	}
	o.setScanoutReason(reason)
	if reason != "" {
		return 0, c, colorBypass
	}
	return fb, c, mode
}

// overlayFrame decides the overlay of a frame: the window, and the scene
// the renderer composes (the window left out). A zero overlayWin means
// none; the reason is logged on change. The returned scene's Windows is
// valid until the next call: it reuses o.restWindows (its consumers, Render,
// ExcludedScene and the capture tracking, run synchronously before then).
func (o *Output) overlayFrame(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) (overlayWin, ports.Scene) {
	reason := "no_plane"
	var ov overlayWin
	if o.overlay != nil && o.scanout {
		var w ports.SceneWindow
		var c ports.SurfaceContent
		var mode colorMode
		w, c, mode, reason = overlayCandidate(s, surfaces, o.hdr.on, o.overlay.pipeline)
		if reason == "" && mode != colorBypass {
			if ok, known := o.overlay.verdict(c.DMABuf.Format, o.cursorShown()); known && !ok {
				reason = "color_refused"
			}
		}
		if reason == "" {
			var fb uint32
			if fb, reason = o.overlayFB(c.DMABuf, time.Now()); reason == "" {
				scale := s.Scale
				if scale <= 0 {
					scale = 1
				}
				ov = overlayWin{id: w.ID, fb: fb, buf: c.DMABuf.ID, w: c.Width, h: c.Height,
					rect:    ports.Rect{X: int(float64(w.Rect.X) * scale), Y: int(float64(w.Rect.Y) * scale), W: c.Width, H: c.Height},
					acquire: dupFence(c.Acquire), color: colorUse{mode: mode, format: c.DMABuf.Format}}
			}
		}
	}
	o.setOverlayReason(reason)
	if ov.id == 0 {
		return overlayWin{}, s
	}
	// The composed frame leaves the window out: the overlay shows it.
	rest := s
	o.restWindows = o.restWindows[:0]
	for _, w := range s.Windows {
		if w.ID != ov.id {
			o.restWindows = append(o.restWindows, w)
		}
	}
	rest.Windows = o.restWindows
	return ov, rest
}

// frameDecision selects the zero-copy path or the scene to compose.
type frameDecision struct {
	fb       uint32
	content  ports.SurfaceContent
	rect     planeRect
	color    colorMode // how the primary plane shows fb
	overlay  overlayWin
	composed ports.Scene
}

// composeFrame chooses overlay or composition after direct scanout cannot commit.
// The caller owns the returned overlay fence.
func (o *Output) composeFrame(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) frameDecision {
	ov, rest := o.overlayFrame(s, surfaces)
	if ov.fb != 0 {
		return frameDecision{overlay: ov, composed: rest}
	}
	return frameDecision{composed: s}
}

// decideFrame tries direct scanout, then overlay, then composition.
// Captures require composition; the caller owns the returned overlay fence.
func (o *Output) decideFrame(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, capture bool) frameDecision {
	if capture {
		o.setScanoutReason("capture")
		o.setOverlayReason("capture")
		return frameDecision{composed: s}
	}
	fb, c, mode := o.scanoutFrame(s, surfaces)
	if fb != 0 {
		return frameDecision{fb: fb, content: c, rect: scanoutRect(c, o.Width(), o.Height()), color: mode}
	}
	return o.composeFrame(s, surfaces)
}

// setScanoutReason logs a direct-scanout transition once.
func (o *Output) setScanoutReason(reason string) {
	if reason != o.reason {
		o.log.Info().Str("component", "render").Bool("direct_scanout", reason == "").Str("reason", reason).Str("connector", o.conn.name).Msg("scanout")
		o.reason = reason
	}
}

// setOverlayReason logs an overlay transition once.
func (o *Output) setOverlayReason(reason string) {
	if reason != o.overlayReason {
		o.log.Info().Str("component", "render").Bool("overlay", reason == "").Str("reason", reason).Str("connector", o.conn.name).Msg("overlay")
		o.overlayReason = reason
	}
}

// clientFB is a client buffer imported as a KMS framebuffer.
type clientFB struct {
	fbID uint32
	last time.Time // when last wanted on screen
	// failed is the reason KMS refused the buffer; it is not retried.
	failed string
	// noAsync: KMS refused an async flip to it; it flips at vblank.
	noAsync                     bool
	scaleTested, scaleFailed    planeRect
	scaleRefused, scaleTestedOK bool
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
			o.log.Info().Str("component", "render").Err(err).Uint32("format", b.Format).Uint64("modifier", b.Modifier).Msg("scanout import failed")
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

// isYUVFormat identifies video formats which are always composed, never scanned out.
func isYUVFormat(format uint32) bool {
	return format == fourccNV12 || format == fourccP010
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
	fourccNV12 = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	fourccP010 = 'P' | '0'<<8 | '1'<<16 | '0'<<24
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
