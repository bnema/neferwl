package drm

import (
	"errors"
	"os"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Overlay plane: a window drawn from one opaque dmabuf, with nothing but
// the cursor above it, goes on an overlay plane at its place on screen;
// the rest of the scene is composed into the primary plane without it.
// The client buffer is shown with no GPU pass, like direct scanout, but
// the window need not cover the output. A refusal (TEST_ONLY) keeps the
// window composed and is cached per buffer.

// pickOverlay takes an overlay plane above the primary one (zpos), when
// the output has one.
func (o *Output) pickOverlay() {
	pz, ok := o.primary.zpos()
	if !ok {
		return
	}
	for _, p := range o.stray {
		if p.typ != planeOverlay {
			continue
		}
		if z, ok := p.zpos(); ok && z > pz {
			o.overlay = p
			return
		}
	}
}

// zpos is the plane's zpos value when known.
func (p *plane) zpos() (uint64, bool) {
	if p.props["zpos"] == 0 {
		return 0, false
	}
	return p.zposValue, true
}

// overlayWin is the window on the overlay plane: its buffer and where it
// shows, in physical pixels.
type overlayWin struct {
	id   ports.WindowID
	fb   uint32
	buf  uint64
	rect ports.Rect // CRTC rect
	w, h int        // buffer size
	// color is how the plane's colour pipeline shows the buffer.
	color colorUse
	// acquire is the client's explicit-sync fence, a duplicate owned by
	// the frame loop (closed after the commit); nil without one.
	acquire *os.File
}

// overlayCandidate finds the one window the overlay can show: an opaque
// dmabuf at integer physical coordinates, drawn 1:1, with no other window
// or layer above it, and the plane colour mode it needs. reason is why none.
func overlayCandidate(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, hdrOn bool, pipeline *colorPipeline) (ports.SceneWindow, ports.SurfaceContent, colorMode, string) {
	if s.Transform != 0 {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "output_transform"
	}
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	var pick *ports.SceneWindow
	var mode colorMode
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		if w.Popup {
			// Popups draw after every window, wherever they are listed.
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "window_above"
		}
		c := surfaces[w.ID]
		// A viewport crop is composed: the plane would show the whole
		// buffer, and so is a colour the plane cannot show (raw PQ values
		// on an SDR output, SDR on HDR without a pipeline). A dimmed
		// window (a peeking stashed one) needs the veil drawn over it, a
		// fading one its translucency, and one with the focus effect its
		// lift; an overview preview is drawn smaller than its buffer.
		if w.Dim <= 0 && w.Fade <= 0 && w.FocusEffect <= 0 && w.Preview <= 0 && c.DMABuf != nil && !isYUVFormat(c.DMABuf.Format) && c.Opaque && len(c.Children) == 0 && c.Transform == 0 && !cropped(c) {
			if m, why := planeColor(c.Color, c.DMABuf.Format, c.Opaque, hdrOn, pipeline); why == "" {
				pick, mode = w, m
				continue
			}
		}
		if pick != nil {
			// Something is drawn above the candidate.
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "window_above"
		}
	}
	if pick == nil {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "no_candidate"
	}
	if s.Dim > 0 {
		// The renderer composes the veil (under the first float, or under
		// every window with DimBehind): a window on the plane would skip it.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "dim"
	}
	if len(s.DropHints) > 0 {
		// Drag hints are drawn over the windows.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "drop_hint"
	}
	for _, l := range s.Layers {
		if l.Layer >= ports.LayerTop && l.Rect.W > 0 && l.Rect.H > 0 {
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "layer_above"
		}
	}
	if !pick.Fullscreen && len(s.Separators) > 0 {
		// The overlay shows the buffer alone; the lines would go with it.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "border"
	}
	if clip := s.WorkspaceClip; clip != (ports.Rect{}) &&
		(pick.Rect.X < clip.X || pick.Rect.Y < clip.Y || pick.Rect.X+pick.Rect.W > clip.X+clip.W || pick.Rect.Y+pick.Rect.H > clip.Y+clip.H) {
		// The plane cannot apply the workspace's logical viewport crop.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "workspace_clip"
	}
	c := surfaces[pick.ID]
	pw, ph := float64(pick.Rect.W)*scale, float64(pick.Rect.H)*scale
	if float64(c.Width) != pw || float64(c.Height) != ph || c.Geometry != (ports.Rect{}) && (c.Geometry.X != 0 || c.Geometry.Y != 0) {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "scaled"
	}
	x, y := float64(pick.Rect.X)*scale, float64(pick.Rect.Y)*scale
	if x != float64(int(x)) || y != float64(int(y)) {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "fractional_position"
	}
	return *pick, c, mode, ""
}

// overlayProps puts ov on the overlay plane, or turns it off.
func (o *Output) overlayProps(req *atomicReq, ov overlayWin) {
	p := o.overlay
	if p == nil {
		return
	}
	if ov.fb == 0 {
		req.set(p.id, p.prop("FB_ID"), 0)
		req.set(p.id, p.prop("CRTC_ID"), 0)
		o.overlayColorProps(req, colorBypass)
		return
	}
	o.overlayColorProps(req, ov.color.mode)
	req.set(p.id, p.prop("FB_ID"), uint64(ov.fb))
	req.set(p.id, p.prop("CRTC_ID"), uint64(o.crtc))
	req.set(p.id, p.prop("SRC_X"), 0)
	req.set(p.id, p.prop("SRC_Y"), 0)
	req.set(p.id, p.prop("SRC_W"), uint64(ov.w)<<16)
	req.set(p.id, p.prop("SRC_H"), uint64(ov.h)<<16)
	req.set(p.id, p.prop("CRTC_X"), uint64(int64(ov.rect.X)))
	req.set(p.id, p.prop("CRTC_Y"), uint64(int64(ov.rect.Y)))
	req.set(p.id, p.prop("CRTC_W"), uint64(ov.rect.W))
	req.set(p.id, p.prop("CRTC_H"), uint64(ov.rect.H))
	if ov.acquire != nil {
		// KMS waits for the client's GPU work before scanning it out.
		req.set(p.id, p.prop("IN_FENCE_FD"), uint64(ov.acquire.Fd()))
	}
}

// close releases the frame's duplicate of the acquire fence.
func (ov overlayWin) close() {
	if ov.acquire != nil {
		ov.acquire.Close()
	}
}

// testOverlay asks KMS whether it takes the frame with ov on the overlay,
// the cursor at its current place included. A refusal is cached on the
// buffer; one that the plane's colour pipeline causes (the same frame passes
// with Bypass) is cached on the plane for the buffer format and the cursor
// state instead.
func (o *Output) testOverlay(fb uint32, ov overlayWin) bool {
	err := o.probeOverlay(fb, ov, ov.color.mode)
	if err == nil {
		return true
	}
	if ov.color.mode != colorBypass && refused(err) && o.probeOverlay(fb, ov, colorBypass) == nil {
		o.overlay.setVerdict(ov.color.format, o.cursorShown(), false)
		o.setOverlayReason("color_refused")
		o.renderLog.Info().Err(err).Uint32("format", ov.color.format).Str("connector", o.conn.name).Msg("overlay colour pipeline refused")
		return false
	}
	if cfb := o.clientFBs[ov.buf]; cfb != nil && refused(err) {
		cfb.overlayFailed = "overlay_refused"
	}
	o.setOverlayReason("overlay_refused")
	o.renderLog.Info().Err(err).Str("connector", o.conn.name).Msg("overlay refused")
	return false
}

// probeOverlay is a TEST_ONLY of the frame: the composed image on the
// primary plane (Bypass), ov on the overlay with colour mode, the cursor.
// Both planes' colour state is written whatever they applied: the test must
// not rely on the state of the last commit.
func (o *Output) probeOverlay(fb uint32, ov overlayWin, mode colorMode) error {
	req := &o.probeReq // no frame request is alive: the frame commit follows
	req.reset()
	o.primaryProps(req, fb)
	o.primary.colorProps(req, colorBypass, 0, 0)
	o.overlayProps(req, ov)
	o.overlay.colorProps(req, mode, o.colorMult, o.ctmBlob)
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, o.cursor.desired())
	}
	return o.k.commit(req, atomicTestOnly, 0)
}

// errOverlayDropped: a commit was refused with the overlay on; the frame is
// composed again without it.
var errOverlayDropped = errors.New("overlay dropped")

// overlayConflict handles a commit refused with buffer buf on the overlay:
// the buffer leaves the overlay for good (e.g. the cursor moved where the
// plane forbids it) and the frame is composed. It reports whether it did.
func (o *Output) overlayConflict(err error, buf uint64) bool {
	if buf == 0 || !errors.Is(err, unix.EINVAL) {
		return false
	}
	if cfb := o.clientFBs[buf]; cfb != nil {
		cfb.overlayFailed = "cursor_conflict"
	}
	o.setOverlayReason("cursor_conflict")
	return true
}
