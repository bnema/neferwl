package drm

import (
	"errors"
	"os"
	"time"

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
	// acquire is the client's explicit-sync fence, a duplicate owned by
	// the frame loop (closed after the commit); nil without one.
	acquire *os.File
}

// overlayCandidate finds the one window the overlay can show: an opaque
// dmabuf at integer physical coordinates, drawn 1:1, with no other window
// or layer above it. reason is why none.
func overlayCandidate(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) (ports.SceneWindow, ports.SurfaceContent, string) {
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	var pick *ports.SceneWindow
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		if w.Popup {
			// Popups draw after every window, wherever they are listed.
			return ports.SceneWindow{}, ports.SurfaceContent{}, "window_above"
		}
		c := surfaces[w.ID]
		if c.DMABuf != nil && !isYUVFormat(c.DMABuf.Format) && c.Opaque && len(c.Children) == 0 {
			pick = w
			continue
		}
		if pick != nil {
			// Something is drawn above the candidate.
			return ports.SceneWindow{}, ports.SurfaceContent{}, "window_above"
		}
	}
	if pick == nil {
		return ports.SceneWindow{}, ports.SurfaceContent{}, "no_candidate"
	}
	for _, l := range s.Layers {
		if l.Layer >= ports.LayerTop && l.Rect.W > 0 && l.Rect.H > 0 && !(pick.Fullscreen && l.Layer == ports.LayerTop) {
			return ports.SceneWindow{}, ports.SurfaceContent{}, "layer_above"
		}
	}
	if !pick.Fullscreen && len(s.Separators) > 0 {
		// The overlay shows the buffer alone; the lines would go with it.
		return ports.SceneWindow{}, ports.SurfaceContent{}, "border"
	}
	c := surfaces[pick.ID]
	pw, ph := float64(pick.Rect.W)*scale, float64(pick.Rect.H)*scale
	if float64(c.Width) != pw || float64(c.Height) != ph || c.Geometry != (ports.Rect{}) && (c.Geometry.X != 0 || c.Geometry.Y != 0) {
		return ports.SceneWindow{}, ports.SurfaceContent{}, "scaled"
	}
	x, y := float64(pick.Rect.X)*scale, float64(pick.Rect.Y)*scale
	if x != float64(int(x)) || y != float64(int(y)) {
		return ports.SceneWindow{}, ports.SurfaceContent{}, "fractional_position"
	}
	return *pick, c, ""
}

// overlayFrame decides the overlay of a frame: the window, and the scene
// the renderer composes (the window left out). A zero overlayWin means
// none; the reason is logged on change.
func (o *Output) overlayFrame(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) (overlayWin, ports.Scene) {
	reason := "no_plane"
	var ov overlayWin
	if o.hdrOn {
		reason = "hdr"
	} else if o.overlay != nil && o.scanout {
		var w ports.SceneWindow
		var c ports.SurfaceContent
		w, c, reason = overlayCandidate(s, surfaces)
		if reason == "" {
			var fb uint32
			if fb, reason = o.overlayFB(c.DMABuf, time.Now()); reason == "" {
				scale := s.Scale
				if scale <= 0 {
					scale = 1
				}
				ov = overlayWin{id: w.ID, fb: fb, buf: c.DMABuf.ID, w: c.Width, h: c.Height,
					rect:    ports.Rect{X: int(float64(w.Rect.X) * scale), Y: int(float64(w.Rect.Y) * scale), W: c.Width, H: c.Height},
					acquire: dupFence(c.Acquire)}
			}
		}
	}
	if reason != o.overlayReason {
		o.log.Info().Str("component", "render").Bool("overlay", reason == "").Str("reason", reason).Str("connector", o.conn.name).Msg("overlay")
		o.overlayReason = reason
	}
	if ov.fb == 0 {
		return overlayWin{}, s
	}
	// The composed frame leaves the window out: the overlay shows it.
	rest := s
	rest.Windows = make([]ports.SceneWindow, 0, len(s.Windows))
	for _, w := range s.Windows {
		if w.ID != ov.id {
			rest.Windows = append(rest.Windows, w)
		}
	}
	return ov, rest
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
		return
	}
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
// buffer.
func (o *Output) testOverlay(fb uint32, ov overlayWin) bool {
	req := &atomicReq{}
	o.primaryProps(req, fb)
	o.overlayProps(req, ov)
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, o.cursor.desired())
	}
	err := o.k.commit(req, atomicTestOnly, 0)
	if err == nil {
		return true
	}
	if cfb := o.clientFBs[ov.buf]; cfb != nil && refused(err) {
		cfb.overlayFailed = "overlay_refused"
	}
	o.log.Info().Str("component", "render").Err(err).Str("connector", o.conn.name).Msg("overlay refused")
	return false
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
	o.log.Info().Str("component", "render").Bool("overlay", false).Str("reason", "cursor_conflict").Str("connector", o.conn.name).Msg("overlay")
	o.overlayReason = "cursor_conflict"
	return true
}
