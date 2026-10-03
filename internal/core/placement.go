package core

import (
	"image"

	"github.com/bnema/neferwl/internal/ports"
)

// placeItem is one output to place: its connector name, logical size, the
// explicit origin when one is set (pos: explicit origin set at runtime by
// wlr-output-management, not a config key) and the output.<name> relation to
// another output.
type placeItem struct {
	name   string
	w, h   int
	pos    *image.Point
	anchor ports.OutputAnchor
}

// placeOutputs returns the logical origin of every item, in item order.
// Precedence: an explicit position, then the relation to another placed
// output, then automatic placement: from the rightmost edge placed so far
// (never left of 0), at y=0. Automatic placement runs only when no relation
// can be resolved, so an anchored output never lands on an automatic one.
// A relation to an absent output, or in a cycle, falls back to automatic.
func placeOutputs(items []placeItem) []image.Point {
	out := make([]image.Point, len(items))
	done := make([]bool, len(items))
	left := len(items)
	for i, it := range items {
		if it.pos != nil {
			out[i], done[i] = *it.pos, true
			left--
		}
	}
	anchored := func(i int) bool { return !done[i] && items[i].anchor.Relation != ports.RelationNone }
	for left > 0 {
		for progress := true; progress; {
			progress = false
			for i, it := range items {
				if !anchored(i) {
					continue
				}
				for j, ref := range items {
					if !done[j] || ref.name == "" || ref.name != it.anchor.To {
						continue
					}
					out[i], done[i] = relativeTo(it, ref, out[j]), true
					left--
					progress = true
					break
				}
			}
		}
		if left == 0 {
			break
		}
		next := -1
		for i := range items {
			if !done[i] && (next < 0 || (anchored(next) && !anchored(i))) {
				next = i
			}
		}
		x := 0
		for i, it := range items {
			if done[i] {
				x = max(x, out[i].X+it.w)
			}
		}
		out[next], done[next] = image.Point{X: x}, true
		left--
	}
	return out
}

// relativeTo is the origin of it placed against ref, whose origin is at.
func relativeTo(it, ref placeItem, at image.Point) image.Point {
	off := it.anchor.Offset
	switch it.anchor.Relation {
	case ports.RelationRightOf:
		return image.Point{X: at.X + ref.w, Y: at.Y + off}
	case ports.RelationLeftOf:
		return image.Point{X: at.X - it.w, Y: at.Y + off}
	case ports.RelationAbove:
		return image.Point{X: at.X + off, Y: at.Y - it.h}
	default: // RelationBelow
		return image.Point{X: at.X + off, Y: at.Y + ref.h}
	}
}
