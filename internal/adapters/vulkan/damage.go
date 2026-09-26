package vulkan

import (
	"image"

	"github.com/bnema/nefertty/internal/ports"
)

// Damage: an output target keeps what it holds (scene Seq, each window's
// content Seq). When the scene is the same, only the region changed since
// then is redrawn: every quad is clipped to it on the CPU and the pass
// keeps the rest of the image. A changed scene, a target never drawn or
// a history too short redraws everything.

// damageRegion collects the region of a target to redraw this frame.
type damageRegion struct {
	held  *target // nil: redraw everything
	area  image.Rectangle
	seen  map[ports.WindowID]bool
	bound image.Rectangle
}

func newDamage(tg *target, s ports.Scene, bounds image.Rectangle) *damageRegion {
	d := &damageRegion{seen: map[ports.WindowID]bool{}, bound: bounds}
	// Seq 0 is a scene core did not number (setup, tests): redraw all.
	if tg.valid && s.Seq != 0 && tg.sceneSeq == s.Seq {
		d.held = tg
	}
	return d
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
	if d.held == nil || d.seen[id] {
		return
	}
	d.seen[id] = true
	held, ok := d.held.seqs[id]
	if ok && held == c.Seq {
		return
	}
	if !ok || c.Empty() || len(c.Children) > 0 {
		d.add(rect)
		return
	}
	rects, known := c.DamageSince(held)
	if !known {
		d.add(rect)
		return
	}
	// Mapped by content, called next: nothing more here.
	_ = rects
}

// content maps a root surface's buffer damage onto full (where its w×h
// buffer is drawn), clipped to dst. Scaled buffers filter linearly: one
// more pixel around each rect.
func (d *damageRegion) content(id ports.WindowID, c *ports.SurfaceContent, full, dst image.Rectangle) {
	if d.held == nil {
		return
	}
	held, ok := d.held.seqs[id]
	if !ok || held == c.Seq || len(c.Children) > 0 {
		return
	}
	rects, known := c.DamageSince(held)
	if !known || c.Width <= 0 || c.Height <= 0 {
		return
	}
	pad := 0
	if full.Dx() != c.Width || full.Dy() != c.Height {
		pad = 1
	}
	for _, r := range rects {
		x0 := full.Min.X + r.X*full.Dx()/c.Width - pad
		y0 := full.Min.Y + r.Y*full.Dy()/c.Height - pad
		x1 := full.Min.X + ((r.X+r.W)*full.Dx()+c.Width-1)/c.Width + pad
		y1 := full.Min.Y + ((r.Y+r.H)*full.Dy()+c.Height-1)/c.Height + pad
		d.add(image.Rect(x0, y0, x1, y1).Intersect(dst))
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

// hold records what the target holds after this frame.
func (tg *target) hold(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent) {
	tg.valid, tg.sceneSeq = true, s.Seq
	tg.seqs = make(map[ports.WindowID]uint64, len(contents))
	for id, c := range contents {
		tg.seqs[id] = c.Seq
	}
}
