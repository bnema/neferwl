package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// The drop preview of a tile drag (Scene.DropPreview): the zone under the
// pointer in Border.Active, the action's label and, on an output that does
// not show the dragged window, a thumbnail of it by the pointer. With
// animations on the zone slides to a new anchor and a hover edge's fill
// ramps up while its timer runs; off, both jump.
const (
	dropFill    = 0.18
	dropFillLow = 0.06
	// dropSlide is the zone slide, times Animations.Slowdown.
	dropSlide = 100 * time.Millisecond
	// dropRamp is the hover edge fill ramp: the hover timer, not slowed.
	dropRamp = dragHoverFirst
	// The thumbnail is a quarter of the window's placed size, at least
	// dropThumbMinW wide, its top-left at the pointer plus dropThumbOffset.
	dropThumbScale  = 0.25
	dropThumbMinW   = 96
	dropThumbOffset = 12
)

// dropAnim is the sampled state of the drop preview, owned by the core
// loop: the zone slides from from to to since start, and the fill ramps
// since ramp.
type dropAnim struct {
	from, to Rect
	start    time.Time
	sliding  bool
	ramp     time.Time
	ramping  bool
	// shown is the zone of the last sample.
	shown Rect
	// zoom is the thumbnail's content scale, 0 without one.
	zoom float64
	// layout is the dragged window's workspace layout scratch.
	layout []Placement
}

// aimDropPreview starts the preview's motion toward the target retarget
// just set, old the target before it.
func (c *Core) aimDropPreview(old dropTarget) {
	d := c.drag
	t, an := d.target, &d.anim
	if t.a.Kind == AnchorNone {
		an.sliding, an.ramping = false, false
		return
	}
	now := c.now()
	if c.animOn() && old.a.Kind != AnchorNone && old.screen == t.screen && an.shown != t.a.Rect {
		an.from, an.start, an.sliding = an.shown, now, true
	} else {
		an.sliding, an.shown = false, t.a.Rect
	}
	an.to = t.a.Rect
	if old.a != t.a {
		an.ramp = now
		an.ramping = c.animOn() && t.a.Kind == AnchorEdge && t.a.Action.Hover()
	}
}

// dropAnimating reports whether the preview slides or ramps on sc (any
// output when sc is nil).
func (c *Core) dropAnimating(sc *screen) bool {
	d := c.drag
	return d != nil && !d.float && d.target.a.Kind != AnchorNone &&
		(d.anim.sliding || d.anim.ramping) && (sc == nil || d.target.screen == sc)
}

// advanceDropPreview samples the preview at now for this publish. It replaces
// d.target.preview only when its content changes: a published preview is
// immutable, and an unchanged one is shared by the next scene.
func (c *Core) advanceDropPreview(now time.Time) {
	d := c.drag
	if d == nil {
		return
	}
	if d.float || d.target.a.Kind == AnchorNone || d.target.screen == nil {
		d.target.preview = nil
		return
	}
	t, an := &d.target, &d.anim
	r := an.to
	if an.sliding {
		dur := time.Duration(float64(dropSlide) * c.cfg.Animations.Slowdown)
		if f := float64(now.Sub(an.start)) / float64(dur); c.animOn() && dur > 0 && f < 1 {
			r = lerpRect(an.from, an.to, max(f, 0))
		} else {
			an.sliding = false
		}
	}
	an.shown = r
	fill := dropFill
	if an.ramping {
		if f := float64(now.Sub(an.ramp)) / float64(dropRamp); c.animOn() && f < 1 {
			fill = dropFillLow + (dropFill-dropFillLow)*max(f, 0)
		} else {
			an.ramping = false
		}
	}
	sc := t.screen
	p := domain.DropPreview{Rect: r, Fill: fill, Label: t.a.Action.Label(), LabelPx: c.dropLabelPx(sc)}
	if t.a.Kind == AnchorWindow && t.ws.Gap() > 0 {
		p.Radius = c.cfg.Border.Radius
	}
	an.zoom = 0
	if !c.dropShown(sc, d.id) {
		p.ThumbRect, an.zoom = c.dropThumb(sc, d.id, &an.layout)
		if an.zoom > 0 {
			p.Thumb = d.id
		}
	}
	if old := t.preview; old != nil && *old == p {
		return
	}
	// Only a changed preview escapes to the heap.
	np := new(domain.DropPreview)
	*np = p
	t.preview = np
}

// dropLabelPx is the label font size on sc, physical px: the hint size
// rule (core/hint.go hintLabelSize).
func (c *Core) dropLabelPx(sc *screen) float64 {
	scale := sc.scale
	if scale <= 0 {
		scale = 1
	}
	return ports.HintFontPixels(sc.info.Width, sc.info.Height, sc.info.PhysicalW, sc.info.PhysicalH, scale, c.cfg.Hint.Size)
}

// dropShown reports whether sc draws window id at its size.
func (c *Core) dropShown(sc *screen, id WindowID) bool {
	view := sc.mon.viewport()
	for _, pl := range sc.shown {
		if pl.ID == id {
			return !pl.Hidden && pl.Preview == 0 && view.shows(pl)
		}
	}
	return false
}

// dropThumb is the thumbnail rect of id on sc, output-local, and its
// content scale; zero when id has no placed size. buf is layout scratch.
func (c *Core) dropThumb(sc *screen, id WindowID, buf *[]Placement) (Rect, float64) {
	_, w := c.screenOf(id)
	if w == nil {
		return Rect{}, 0
	}
	*buf = w.AppendLayout((*buf)[:0])
	var nat Rect
	for _, pl := range *buf {
		if pl.ID == id {
			nat = pl.Rect
			break
		}
	}
	if nat.W <= 0 || nat.H <= 0 {
		return Rect{}, 0
	}
	o := sc.mon.Output()
	tw := max(int(math.Round(float64(nat.W)*dropThumbScale)), dropThumbMinW)
	zoom := float64(tw) / float64(nat.W)
	th := max(int(math.Round(float64(nat.H)*zoom)), 1)
	x := int(c.cursorX) - sc.x + dropThumbOffset
	y := int(c.cursorY) - sc.y + dropThumbOffset
	x = max(min(x, o.W-tw), 0)
	y = max(min(y, o.H-th), 0)
	return Rect{X: x, Y: y, W: tw, H: th}, zoom
}

// dropProxy adds the thumbnail window of the drag preview of sc to ws,
// keeping its IDs unique: it replaces a hidden entry of the window (its
// workspace switched away) and is not added when ws draws it.
func (c *Core) dropProxy(ws []domain.SceneWindow, sc *screen) []domain.SceneWindow {
	d := c.drag
	if d == nil || d.target.screen != sc || d.target.preview == nil || d.target.preview.Thumb == 0 || d.anim.zoom <= 0 {
		return ws
	}
	p := d.target.preview
	proxy := domain.SceneWindow{ID: p.Thumb, Proxy: true, Rect: p.ThumbRect, Zoom: d.anim.zoom}
	for i, w := range ws {
		if w.ID == p.Thumb {
			if w.Hidden && !w.Popup {
				ws[i] = proxy
			}
			return ws
		}
	}
	return append(ws, proxy)
}

// lerpRect is a + (b-a)·f, rounded.
func lerpRect(a, b Rect, f float64) Rect {
	l := func(x, y int) int { return x + int(math.Round(float64(y-x)*f)) }
	return Rect{X: l(a.X, b.X), Y: l(a.Y, b.Y), W: l(a.W, b.W), H: l(a.H, b.H)}
}
