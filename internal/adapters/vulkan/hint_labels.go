package vulkan

import (
	"image"
	"math"

	"github.com/bnema/neferwl/internal/adapters/hintlabel"
)

// hintLabels draws the keyboard hint labels (ports.Scene.HintLabels) over
// everything, after the capture indicators. Each label is rasterized on the CPU
// (hintlabel) at the scene's scale and drawn from the SHM copy path: the GPU
// buffer is cached under a key naming the label, so a label that stays the
// same uploads once. Captures are rendered from scenes without labels.
func (w *sceneWalk) hintLabels() {
	if len(w.s.HintLabels) == 0 {
		return
	}
	r := w.r
	hs := w.s.HintStyle
	bg, fg := hintlabel.Colors(w.s.Border.Active, hs.Background, hs.Foreground)
	style := hintlabel.Style{FontPx: hintlabel.FontPixels(hs.Width, hs.Height, hs.PhysicalW, hs.PhysicalH, w.scale, hs.Size), Background: bg, Foreground: fg}
	for _, l := range w.s.HintLabels {
		pix, lw, lh := r.labels.Label(l.Text, l.Typed, w.scale, style)
		if lw <= 0 || lh <= 0 {
			continue
		}
		// Anchored outside the target's top-left corner, overlapping it by a
		// quarter of the label height, then kept inside the output.
		inset := int(math.Round(0.25 * float64(lh)))
		x := min(max(w.phys(l.Rect.X)-lw+inset, w.bounds.Min.X), max(w.bounds.Max.X-lw, w.bounds.Min.X))
		y := min(max(w.phys(l.Rect.Y)-lh+inset, w.bounds.Min.Y), max(w.bounds.Max.Y-lh, w.bounds.Min.Y))
		rect := image.Rect(x, y, x+lw, y+lh)
		if rect.Intersect(w.bounds).Empty() {
			continue
		}
		key := shmKey{label: l.Text, typed: l.Typed, scale: w.scale, style: style}
		st := shmState{w: lw, h: lh, seq: 1}
		c := r.shmCached(key, st)
		if c == nil {
			var err error
			if c, err = r.shmCopyFor(key, st, pix, 0, lw*4, nil); err != nil {
				continue
			}
		}
		dr := r.contentDraw(rect, rect, lw, lh, [4]float32{}, 0, modeBuffer, true)
		dr.pc.color[3] = 1
		dr.shm = c
		dr.pc.buf = [4]uint32{0, uint32(lw), uint32(lh), 0}
		w.draws = append(w.draws, dr)
	}
}
