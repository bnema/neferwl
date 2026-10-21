package drm

import (
	"errors"
	"os"
	"slices"

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
// dmabuf at integer physical coordinates, drawn 1:1, with no other window,
// popup, border line or layer drawn over it, and the plane colour mode it
// needs. What is above it elsewhere on screen stays on the primary plane,
// which the overlay covers only over the window. reason is why none.
//
// kept is scanoutCandidate's: a window drawn from a withdrawn content is
// composed, never put on the plane.
func overlayCandidate(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, hdrOn bool, pipeline *colorPipeline, kept func(ports.WindowID) bool) (ports.SceneWindow, ports.SurfaceContent, colorMode, string) {
	// A hardware plane would bypass the compositor-owned flash border.
	if s.PositionHint.Shown() {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "position_hint"
	}
	if s.Flash != nil {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "focus_flash"
	}
	if len(s.HintLabels) > 0 {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "hint_labels"
	}
	if s.Transform != 0 {
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "output_transform"
	}
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	var pick *ports.SceneWindow
	var pickAt int
	var mode colorMode
	for i := range s.Windows {
		w := &s.Windows[i]
		if !s.Draws(*w) {
			// Not drawn (a tile wholly under a panel included): neither
			// a candidate nor over one.
			continue
		}
		if w.Popup {
			// Popups draw after every window, wherever they are listed:
			// checked once the candidate is known.
			continue
		}
		c := surfaces[w.ID]
		// A viewport crop is composed: the plane would show the whole
		// buffer, and so is a colour the plane cannot show (raw PQ values
		// on an SDR output, SDR on HDR without a pipeline). A dimmed
		// window (a peeking stashed one) needs the veil drawn over it, a
		// fading one its translucency; an overview preview is drawn smaller
		// than its buffer.
		if w.Radius == 0 && w.Dim <= 0 && w.Fade <= 0 && w.Zoom <= 0 && w.Preview <= 0 && c.DMABuf != nil && !isYUVFormat(c.DMABuf.Format) && c.Opaque && len(c.Children) == 0 && c.Transform == 0 && !cropped(c) && (kept == nil || !kept(w.ID)) {
			if m, why := planeColor(c.Color, c.DMABuf.Format, c.Opaque, hdrOn, pipeline); why == "" {
				pick, pickAt, mode = w, i, m
				continue
			}
		}
		if pick != nil && w.Rect.Overlaps(pick.Rect) {
			// Something is drawn over the candidate.
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "window_above"
		}
	}
	if pick != nil {
		for i := range s.Windows {
			if w := &s.Windows[i]; w.Popup && s.Draws(*w) && w.Rect.Overlaps(pick.Rect) {
				return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "window_above"
			}
		}
	}
	if pick == nil {
		if kept != nil && len(s.Windows) > 0 {
			// The one window that could have gone on the plane fades out
			// after its close: composed from its kept content.
			for i := range s.Windows {
				if w := &s.Windows[i]; s.Draws(*w) && kept(w.ID) {
					return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "leaving"
				}
			}
		}
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
		if l.Layer >= ports.LayerTop && l.Rect.Overlaps(pick.Rect) {
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "layer_above"
		}
	}
	if !pick.Fullscreen {
		if pick.Inset != 0 && s.Border.Width > 0 {
			// The content sits inside the lines, not at the window's rect.
			return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "border"
		}
		for _, sep := range s.Separators {
			if sep.Rect.Overlaps(pick.Rect) && !linesUnder(s, sep, pick, pickAt) {
				// The overlay shows the buffer alone; the lines would go
				// under it.
				return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "border"
			}
		}
	}
	if clip := s.WorkspaceClip; clip != (ports.Rect{}) && !clip.Contains(pick.Rect) {
		// The plane cannot apply the workspace's logical viewport crop.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "workspace_clip"
	}
	if a, ok := s.TileArea(); ok && pick.Tile() && !a.Contains(pick.Rect) {
		// Nor the cut of a tile at a panel.
		return ports.SceneWindow{}, ports.SurfaceContent{}, colorBypass, "tile_clip"
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

// linesUnder reports whether sep is painted before the candidate at index
// pickAt, so stays composed under the plane: the tile lines and the lines
// of windows listed before it, when the candidate is a float drawn above
// the tiles.
func linesUnder(s ports.Scene, sep ports.Separator, pick *ports.SceneWindow, pickAt int) bool {
	if !pick.Floating || pick.Below {
		return false
	}
	if sep.Window == 0 {
		return true
	}
	for _, w := range s.Windows[:pickAt] {
		if w.ID == sep.Window {
			return true
		}
	}
	return false
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
//
// A frame identical to one KMS took (same images, place, colour mode and
// cursor; the fence does not count) is not probed again: a refusal of the
// commit itself still drops the overlay (overlayConflict).
func (o *Output) testOverlay(fb uint32, ov overlayWin) bool {
	// One cursor snapshot: the input goroutine may move it meanwhile, and
	// what is remembered must be what KMS was asked.
	key := o.overlayProbeOf(fb, ov)
	if slices.Contains(o.overlayPassed[:], key) {
		return true
	}
	err := o.probeOverlay(fb, ov, ov.color.mode, key.cursor)
	if err == nil {
		o.overlayPassed[o.overlayNext] = key
		o.overlayNext = (o.overlayNext + 1) % len(o.overlayPassed)
		return true
	}
	if ov.color.mode != colorBypass && refused(err) && o.probeOverlay(fb, ov, colorBypass, key.cursor) == nil {
		o.overlay.setVerdict(ov.color.format, key.cursor.on, false)
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

// forgetOverlayProbes drops the remembered probes: KMS is asked again.
func (o *Output) forgetOverlayProbes() { clear(o.overlayPassed[:]) }

// overlayProbe is what an overlay probe depends on, fences aside.
type overlayProbe struct {
	primary, overlay uint32
	rect             ports.Rect
	w, h             int
	color            colorUse
	cursor           cursorState
	mult             uint64
	ctm              uint32
}

func (o *Output) overlayProbeOf(fb uint32, ov overlayWin) overlayProbe {
	p := overlayProbe{primary: fb, overlay: ov.fb, rect: ov.rect, w: ov.w, h: ov.h, color: ov.color, mult: o.colorMult, ctm: o.ctmBlob}
	if o.cursor != nil && !o.protected {
		p.cursor = o.cursor.desired()
	}
	return p
}

// probeOverlay is a TEST_ONLY of the frame: the composed image on the
// primary plane (Bypass), ov on the overlay with colour mode, the cursor in
// state cur. Both planes' colour state is written whatever they applied:
// the test must not rely on the state of the last commit.
func (o *Output) probeOverlay(fb uint32, ov overlayWin, mode colorMode, cur cursorState) error {
	req := &o.probeReq // no frame request is alive: the frame commit follows
	req.reset()
	o.primaryProps(req, fb)
	o.primary.colorProps(req, colorBypass, 0, 0)
	o.overlayProps(req, ov)
	o.overlay.colorProps(req, mode, o.colorMult, o.ctmBlob)
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, cur)
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
