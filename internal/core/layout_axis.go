package core

// layoutAxis is a logical navigation axis, independent of rendering.
type layoutAxis uint8

const (
	verticalAxis layoutAxis = iota
	horizontalAxis
)

func (a layoutAxis) span(r Rect) int {
	if a == verticalAxis {
		return r.H
	}
	return r.W
}
func (a layoutAxis) offset(r *Rect, pixels int) {
	if a == verticalAxis {
		r.Y += pixels
	} else {
		r.X += pixels
	}
}
