package vulkan

import (
	"image"

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
	// The active dot is white, opaque and larger, whatever border.active is:
	// a grey accent must not blend it into the other dots of its column.
	for col := range min(int(h.Columns), ports.PositionHintMaxColumns) {
		for row := range min(int(h.Counts[col]), ports.PositionHintMaxRows) {
			color, opacity, d := [3]uint8{190, 190, 200}, float32(0.2), ports.PositionHintDiameter
			if !h.Vertical && col == int(h.ActiveCol) {
				opacity = 0.45
			}
			if col == int(h.ActiveCol) && row == int(h.ActiveRow) {
				color, opacity, d = [3]uint8{255, 255, 255}, 1, ports.PositionHintActiveDiameter
			}
			w.alpha = h.Opacity * opacity
			// Every dot is centered in its PositionHintDiameter cell.
			inset := (ports.PositionHintDiameter - d) / 2
			x := b.X + ports.PositionHintPadding + col*(ports.PositionHintDiameter+ports.PositionHintGap) + inset
			y := b.Y + ports.PositionHintPadding + row*(ports.PositionHintDiameter+ports.PositionHintGap) + inset
			w.hintRound(w.physRect(x, y, d, d), w.phys(d)/2, color)
		}
	}
	w.alpha = alpha
}

// hintRound draws one rounded quad (SDF in the shader), so the pill and each
// dot cost a single draw whatever the scale. Fills are one translucent draw,
// so alpha stays exact.
func (w *sceneWalk) hintRound(r image.Rectangle, radius int, color [3]uint8) {
	radius = min(radius, r.Dx()/2, r.Dy()/2)
	w.fillRounded(r, color, float32(radius))
}
