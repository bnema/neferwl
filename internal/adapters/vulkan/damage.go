package vulkan

import (
	"image"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// Damage: an output target keeps what it holds (scene Seq, each window's
// content Seq). When the scene is the same, only the region changed since
// then is redrawn: every quad is clipped to it on the CPU and the pass
// keeps the rest of the image. A scene with a new Seq redraws only the
// windows and separators that differ from the scene the target holds
// (sceneDelta). A scene that differs in anything else, a target never
// drawn or a history too short redraws everything.

// damageRegion collects the region of a target to redraw this frame and
// what the target will hold after it: the windows drawn, their content
// Seq and rect.
type damageRegion struct {
	held  *target // nil: redraw everything
	area  image.Rectangle
	seen  map[ports.WindowID]bool
	bound image.Rectangle
	drawn map[ports.WindowID]heldWindow
	// rects is scratch for a content's damage rects (reused across frames).
	rects []ports.Rect
}

// heldWindow is a window a target holds: its content Seq and rect.
type heldWindow struct {
	seq  uint64
	rect image.Rectangle
}

func newDamage(tg *target, s ports.Scene, bounds image.Rectangle) *damageRegion {
	d := &damageRegion{seen: map[ports.WindowID]bool{}, bound: bounds, drawn: map[ports.WindowID]heldWindow{}}
	d.hold(tg, s)
	return d
}

// hold decides what the target keeps. Seq 0 is a scene core did not
// number (setup, tests): redraw all. The same Seq keeps everything; a new
// one keeps all but what sceneDelta finds changed.
func (d *damageRegion) hold(tg *target, s ports.Scene) {
	if !tg.valid || s.Seq == 0 {
		return
	}
	if tg.sceneSeq == s.Seq {
		d.held = tg
		return
	}
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	phys := func(r ports.Rect) image.Rectangle {
		p := func(v int) int { return int(math.Round(float64(v) * scale)) }
		return image.Rect(p(r.X), p(r.Y), p(r.X+r.W), p(r.Y+r.H)).Intersect(d.bound)
	}
	// The limit only bounds the CPU cost of clipping every quad.
	limit := d.bound.Dx() * d.bound.Dy() * 9 / 10
	if area, ok := sceneDelta(tg.scene, s, phys, limit); ok {
		d.held, d.area = tg, area
	}
}

// frameDamage reuses the bookkeeping maps. drawn is swapped with the
// target's previous map after hold, so a target never observes mutations.
func (r *Renderer) frameDamage(tg *target, s ports.Scene, bounds image.Rectangle) *damageRegion {
	if r.damageSeen == nil {
		r.damageSeen = make(map[ports.WindowID]bool)
	}
	clear(r.damageSeen)
	if r.damageDrawn == nil {
		r.damageDrawn = make(map[ports.WindowID]heldWindow)
	}
	clear(r.damageDrawn)
	r.damage = damageRegion{seen: r.damageSeen, drawn: r.damageDrawn, bound: bounds, rects: r.damage.rects[:0]}
	r.damage.hold(tg, s)
	return &r.damage
}

// all reports whether the whole target is redrawn.
func (d *damageRegion) all() bool { return d.held == nil }

func (d *damageRegion) add(r image.Rectangle) {
	if d.held != nil {
		d.area = d.area.Union(r.Intersect(d.bound))
	}
}

// window records a window drawn in rect (physical): if its content changed
// and no finer damage comes (content), the whole rect is redrawn.
func (d *damageRegion) window(id ports.WindowID, c ports.SurfaceContent, rect image.Rectangle) {
	if d.seen[id] {
		return
	}
	d.seen[id] = true
	d.drawn[id] = heldWindow{seq: c.Seq, rect: rect}
	if d.held == nil {
		return
	}
	hw, ok := d.held.windows[id]
	held := hw.seq
	if ok && held == c.Seq {
		return
	}
	if !ok || c.Empty() || len(c.Children) > 0 {
		d.add(rect)
		return
	}
	var known bool
	d.rects, known = c.AppendDamageSince(d.rects[:0], held)
	if !known {
		d.add(rect)
	}
	// Otherwise mapped by content, called next: nothing more here.
}

// content maps a root surface's buffer damage onto full (where its w×h
// buffer is drawn), clipped to dst. Scaled buffers filter linearly: one
// more pixel around each rect. A transformed buffer redraws all of dst.
func (d *damageRegion) content(id ports.WindowID, c *ports.SurfaceContent, full, dst image.Rectangle) {
	if d.held == nil {
		return
	}
	hw, ok := d.held.windows[id]
	held := hw.seq
	if !ok || held == c.Seq || len(c.Children) > 0 {
		return
	}
	var known bool
	d.rects, known = c.AppendDamageSince(d.rects[:0], held)
	if !known || c.Width <= 0 || c.Height <= 0 {
		return
	}
	rects := d.rects
	if c.Transform != 0 {
		d.add(dst)
		return
	}
	// Linear filtering spreads a texel over half a buffer pixel around
	// it: scale/2 target pixels, plus one for rounding.
	pad := 0
	if full.Dx() != c.Width || full.Dy() != c.Height {
		s := max(float64(full.Dx())/float64(c.Width), float64(full.Dy())/float64(c.Height))
		pad = int(math.Ceil(s/2)) + 1
	}
	for _, r := range rects {
		x0 := full.Min.X + r.X*full.Dx()/c.Width - pad
		y0 := full.Min.Y + r.Y*full.Dy()/c.Height - pad
		x1 := full.Min.X + ((r.X+r.W)*full.Dx()+c.Width-1)/c.Width + pad
		y1 := full.Min.Y + ((r.Y+r.H)*full.Dy()+c.Height-1)/c.Height + pad
		d.add(image.Rect(x0, y0, x1, y1).Intersect(dst))
	}
}

// finish adds the rects of windows the target holds that this frame does
// not draw (e.g. a window that moved to an overlay plane and back).
func (d *damageRegion) finish() {
	if d.held == nil {
		return
	}
	for id, hw := range d.held.windows {
		if _, ok := d.drawn[id]; !ok {
			d.add(hw.rect)
		}
	}
}

// clip limits the draws to the region; draws outside it are dropped.
func (d *damageRegion) clip(ds []draw) []draw {
	if d.held == nil {
		return ds
	}
	out := ds[:0]
	for _, dr := range ds {
		r := image.Rect(int(dr.pc.rect[0]), int(dr.pc.rect[1]), int(dr.pc.rect[2]), int(dr.pc.rect[3])).Intersect(d.area)
		if r.Empty() {
			continue
		}
		dr.pc.rect = [4]int32{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
		out = append(out, dr)
	}
	return out
}

// hold records what the target holds after this frame: only the windows
// it drew, and the scene with its compared slices copied into the target's
// buffers (zero allocations once they are large enough): the caller may
// rewrite its slices in place later.
func (tg *target) hold(s ports.Scene, d *damageRegion) {
	tg.heldWindows = append(tg.heldWindows[:0], s.Windows...)
	tg.heldSeparators = append(tg.heldSeparators[:0], s.Separators...)
	tg.heldLayers = append(tg.heldLayers[:0], s.Layers...)
	tg.heldHints = append(tg.heldHints[:0], s.DropHints...)
	tg.heldIndicators = append(tg.heldIndicators[:0], s.CaptureIndicators...)
	tg.valid, tg.sceneSeq, tg.scene, tg.windows = true, s.Seq, s, d.drawn
	tg.scene.Windows, tg.scene.Separators, tg.scene.Layers = tg.heldWindows, tg.heldSeparators, tg.heldLayers
	tg.scene.DropHints, tg.scene.CaptureIndicators = tg.heldHints, tg.heldIndicators
	// Only whether they are set matters (any capture redraws everything):
	// sentinels, so no pointer into the caller's capture state is kept.
	tg.scene.Capture, tg.scene.CaptureScene = nil, nil
	if s.Capture != nil {
		tg.scene.Capture = &heldCapture
	}
	if s.CaptureScene != nil {
		tg.scene.CaptureScene = &heldCaptureScene
	}
}

var (
	heldCapture      ports.SceneCapture
	heldCaptureScene ports.Scene
)

// sceneDelta is the region (physical, through phys) where cur differs from
// old, two scenes of one output; ok is false when the whole output must be
// redrawn: anything but the windows and separators differs, the windows
// are not the same list, or the region covers more than limit pixels.
// Each window that differs (any field) adds its old and new rects, each
// separator present in only one scene adds its own. A new Scene field
// belongs here too (see Scene.SameAs, TestSceneSameAsCoversEveryField); a
// new SceneWindow field is checked by TestSceneWindowFieldCount.
func sceneDelta(old, cur ports.Scene, phys func(ports.Rect) image.Rectangle, limit int) (image.Rectangle, bool) {
	if old.Security != cur.Security || old.Output != cur.Output ||
		old.OutputWidth != cur.OutputWidth || old.OutputHeight != cur.OutputHeight ||
		old.Scale != cur.Scale || old.Transform != cur.Transform || old.Off != cur.Off ||
		old.Background != cur.Background || old.Border != cur.Border ||
		old.WorkspaceClip != cur.WorkspaceClip || old.TileClip != cur.TileClip || old.Dim != cur.Dim || old.DimBehind != cur.DimBehind ||
		old.Capture != nil || cur.Capture != nil || old.CaptureScene != nil || cur.CaptureScene != nil ||
		!slices.Equal(old.Layers, cur.Layers) || !slices.Equal(old.DropHints, cur.DropHints) ||
		!slices.Equal(old.CaptureIndicators, cur.CaptureIndicators) ||
		len(old.Windows) != len(cur.Windows) {
		return image.Rectangle{}, false
	}
	// windows() paints the tile lines and the Dim veil under the first
	// window that opens the floats: when that moves, the paint order of the
	// whole output changed, not only the rects of the windows that differ.
	if floatsStart(old) != floatsStart(cur) {
		return image.Rectangle{}, false
	}
	var area image.Rectangle
	for i := range old.Windows {
		a, b := old.Windows[i], cur.Windows[i]
		if a.ID != b.ID {
			return image.Rectangle{}, false
		}
		if a != b {
			area = area.Union(phys(a.Rect)).Union(phys(b.Rect))
		}
	}
	area = area.Union(separatorDelta(old.Separators, cur.Separators, phys))
	if area.Dx()*area.Dy() > limit {
		return image.Rectangle{}, false
	}
	return area, true
}

// separatorDelta is the region where the separator lists differ: a line in
// one list more often than in the other. Core builds the lists in layout
// order (one pass over the placements, sides in a fixed order), so between
// two frames of the same windows they are index-aligned: equal lists cost
// one comparison, and with lists of the same length only the pairs that
// differ by index are counted in the other list (a focus change lights a
// few lines, not all). Lists of different lengths (a line scrolled on or
// off) count every line.
func separatorDelta(old, cur []ports.Separator, phys func(ports.Rect) image.Rectangle) image.Rectangle {
	var area image.Rectangle
	if slices.Equal(old, cur) {
		return area
	}
	if len(old) == len(cur) {
		for i := range old {
			if old[i] == cur[i] {
				continue
			}
			if countSeparator(old, old[i]) != countSeparator(cur, old[i]) {
				area = area.Union(phys(old[i].Rect))
			}
			if countSeparator(old, cur[i]) != countSeparator(cur, cur[i]) {
				area = area.Union(phys(cur[i].Rect))
			}
		}
		return area
	}
	for _, sep := range old {
		if countSeparator(old, sep) != countSeparator(cur, sep) {
			area = area.Union(phys(sep.Rect))
		}
	}
	for _, sep := range cur {
		if countSeparator(old, sep) != countSeparator(cur, sep) {
			area = area.Union(phys(sep.Rect))
		}
	}
	return area
}

func countSeparator(list []ports.Separator, sep ports.Separator) int {
	n := 0
	for _, o := range list {
		if o == sep {
			n++
		}
	}
	return n
}
