package geom

import "github.com/bnema/neferwl/internal/ports"

// BorderZone returns the origin and size of a padded zone between two edges.
func BorderZone(a, b, pad int) (int, int) {
	mid := (a + b) / 2
	lo, hi := min(a, mid-pad), max(b, mid+pad)
	return lo, max(hi-lo, 1)
}

// Union is the smallest rect holding a and b.
func Union(a, b ports.Rect) ports.Rect {
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return ports.Rect{X: x, Y: y, W: max(a.X+a.W, b.X+b.W) - x, H: max(a.Y+a.H, b.Y+b.H) - y}
}
