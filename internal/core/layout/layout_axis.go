package layout

// Axis is a logical navigation axis, independent of rendering.
type Axis uint8

const (
	// Vertical follows the logical Y axis.
	Vertical Axis = iota
	// Horizontal follows the logical X axis.
	Horizontal
)

// Span returns the rectangle’s extent along the axis.
func (a Axis) Span(r Rect) int {
	if a == Vertical {
		return r.H
	}
	return r.W
}

// Offset translates the rectangle along the axis.
func (a Axis) Offset(r *Rect, pixels int) {
	if a == Vertical {
		r.Y += pixels
	} else {
		r.X += pixels
	}
}
