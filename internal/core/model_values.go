package core

// Shared value arithmetic used by presentation, independent of Workspace state.

func union(a, b Rect) Rect {
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return Rect{X: x, Y: y, W: max(a.X+a.W, b.X+b.W) - x, H: max(a.Y+a.H, b.Y+b.H) - y}
}

func carouselSlot(i, at, n int, wraps bool) int {
	if !wraps {
		return i - at
	}
	s := (i - at + n) % n
	if s > (n-1)/2 {
		s -= n
	}
	return s
}

func abs(n int) int { return max(n, -n) }
