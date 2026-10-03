package core

import "image"

// placeItem is one output to place: its connector name, logical size and, when set, the
// explicit origin from output management (output.<name> position).
type placeItem struct {
	name string
	w, h int
	pos  *image.Point
}

// placeOutputs returns the logical origin of every item, in item order.
// Items with an explicit position keep it; the others follow, in order, from
// the rightmost edge placed so far (never left of 0), at y=0.
func placeOutputs(items []placeItem) []image.Point {
	out := make([]image.Point, len(items))
	x := 0
	for i, it := range items {
		if it.pos != nil {
			out[i] = *it.pos
			x = max(x, it.pos.X+it.w)
		}
	}
	for i, it := range items {
		if it.pos == nil {
			out[i] = image.Point{X: x}
			x += it.w
		}
	}
	return out
}
