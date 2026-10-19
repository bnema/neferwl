package vulkan

import (
	"image"
	"math"

	"github.com/bnema/neferwl/internal/ports"
)

func (w *sceneWalk) positionHint() {
	h := w.s.PositionHint
	if !h.Shown() {
		return
	}
	b := h.Bounds(w.s.OutputWidth, w.s.OutputHeight)
	alpha := w.alpha
	w.alpha = h.Opacity * 0.8
	w.hintRound(w.physRect(b.X, b.Y, b.W, b.H), w.phys(ports.PositionHintRadius), [3]uint8{30, 30, 38})
	active := parseColor(w.s.Border.Active)
	for col := range min(int(h.Columns), ports.PositionHintMaxColumns) {
		for row := range min(int(h.Counts[col]), ports.PositionHintMaxRows) {
			color, opacity := [3]uint8{190, 190, 200}, float32(0.3)
			if !h.Vertical && col == int(h.ActiveCol) {
				opacity = 0.65
			}
			if col == int(h.ActiveCol) && row == int(h.ActiveRow) {
				color, opacity = active, 1
			}
			w.alpha = h.Opacity * opacity
			x := b.X + ports.PositionHintPadding + col*(ports.PositionHintDiameter+ports.PositionHintGap)
			y := b.Y + ports.PositionHintPadding + row*(ports.PositionHintDiameter+ports.PositionHintGap)
			w.hintRound(w.physRect(x, y, ports.PositionHintDiameter, ports.PositionHintDiameter), w.phys(ports.PositionHintDiameter)/2, color)
		}
	}
	w.alpha = alpha
}

// Rounded fills use the existing solid instances and batching pipeline. Each
// physical scanline is bounded by the scaled radius; no textures or per-frame
// buffers are allocated. Non-overlapping strips preserve translucent alpha.
func (w *sceneWalk) hintRound(r image.Rectangle, radius int, color [3]uint8) {
	radius = min(radius, r.Dx()/2, r.Dy()/2)
	if radius <= 0 {
		w.fill(r, color)
		return
	}
	w.fill(image.Rect(r.Min.X, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), color)
	for y := range radius {
		dy := float64(radius-y) - 0.5
		inset := radius - int(math.Sqrt(float64(radius*radius)-dy*dy)+0.5)
		w.fill(image.Rect(r.Min.X+inset, r.Min.Y+y, r.Max.X-inset, r.Min.Y+y+1), color)
		w.fill(image.Rect(r.Min.X+inset, r.Max.Y-y-1, r.Max.X-inset, r.Max.Y-y), color)
	}
}
