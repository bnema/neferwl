package vulkan

import (
	"image"
	"math"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"github.com/bnema/neferwl/internal/ports"
)

// overlays draws scene label overlays over everything, after the capture
// indicators. CPU rasters use the SHM copy path, cached by overlay ID, scale
// and style; Seq versions the content held by the GPU's double buffer.
func (w *sceneWalk) overlays() {
	r := w.r
	for _, o := range w.s.Overlays {
		switch o.Kind {
		case ports.OverlayLabel:
		default:
			continue
		}
		bg, fg := overlaydraw.Colors(w.s.Border.Active, o.Style.Background, o.Style.Foreground)
		style := overlaydraw.Style{FontPx: overlaydraw.FontPixels(w.s.Physical.Width, w.s.Physical.Height, w.s.Physical.MMWidth, w.s.Physical.MMHeight, w.scale, o.Style.Size), Background: bg, Foreground: fg}
		pix, lw, lh := r.overlayRaster.Label(o.Text, o.Typed, w.scale, style)
		if lw <= 0 || lh <= 0 {
			continue
		}
		// Anchored outside the target's top-left corner, overlapping it by a
		// quarter of the label height, then kept inside the output.
		inset := int(math.Round(0.25 * float64(lh)))
		x := min(max(w.phys(o.Rect.X)-lw+inset, w.bounds.Min.X), max(w.bounds.Max.X-lw, w.bounds.Min.X))
		y := min(max(w.phys(o.Rect.Y)-lh+inset, w.bounds.Min.Y), max(w.bounds.Max.Y-lh, w.bounds.Min.Y))
		rect := image.Rect(x, y, x+lw, y+lh)
		if rect.Intersect(w.bounds).Empty() {
			continue
		}
		key := shmKey{overlay: o.ID, scale: w.scale, style: style}
		st := shmState{w: lw, h: lh, seq: o.Seq}
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
