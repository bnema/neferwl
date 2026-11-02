package xwm

import "github.com/bnema/neferwl/internal/domain"

const maxRegions = 16

// Region is one output's part of the layout and where it is in X space.
// X = layout - Origin.
type Region struct {
	Layout, X domain.Rect // logical layout rect; X rect in X pixels
}

// Space maps layout coordinates (which may be negative) to X coordinates
// (non-negative: the layout top-left is X (0,0)). Fixed storage, no allocs.
type Space struct {
	regions [maxRegions]Region
	n       int
	dropped int     // outputs beyond maxRegions in the last Set
	ox, oy  int     // layout point that is X (0,0)
	w, h    int     // X screen size: the extents
	scale   float64 // scale of the first output; not part of the change test
}

// Origin is the layout point that is X (0,0): the top-left of the layout
// extents.
func (s *Space) Origin() (ox, oy int) { return s.ox, s.oy }

// Dropped is how many outputs of the last Set did not fit.
func (s *Space) Dropped() int { return s.dropped }

// Set recomputes the mapping and reports whether it changed.
func (s *Space) Set(l domain.Layout) bool {
	var next Space
	n := len(l)
	if n > maxRegions {
		next.dropped = n - maxRegions
		n = maxRegions
	}
	next.n = n
	var maxX, maxY int
	for i := 0; i < n; i++ {
		o := l[i]
		if i == 0 || o.X < next.ox {
			next.ox = o.X
		}
		if i == 0 || o.Y < next.oy {
			next.oy = o.Y
		}
		if i == 0 || o.X+o.Width > maxX {
			maxX = o.X + o.Width
		}
		if i == 0 || o.Y+o.Height > maxY {
			maxY = o.Y + o.Height
		}
	}
	for i := 0; i < n; i++ {
		o := l[i]
		next.regions[i] = Region{
			Layout: domain.Rect{X: o.X, Y: o.Y, W: o.Width, H: o.Height},
			X:      domain.Rect{X: o.X - next.ox, Y: o.Y - next.oy, W: o.Width, H: o.Height},
		}
	}
	next.w, next.h = maxX-next.ox, maxY-next.oy
	if n > 0 {
		next.scale = l[0].Scale
	}
	if next.regions == s.regions && next.n == s.n && next.ox == s.ox && next.oy == s.oy &&
		next.w == s.w && next.h == s.h {
		s.dropped, s.scale = next.dropped, next.scale
		return false
	}
	*s = next
	return true
}

// Scale is the scale of the first output of the layout, 1 when unset.
func (s *Space) Scale() float64 {
	if s.scale <= 0 {
		return 1
	}
	return s.scale
}

// Size is the X screen size.
func (s *Space) Size() (w, h int) { return s.w, s.h }

// WholeOutput reports whether the X rect is exactly one output's, and that
// output's layout origin.
func (s *Space) WholeOutput(x, y, w, h int) (lx, ly int, ok bool) {
	for i := 0; i < s.n; i++ {
		if r := s.regions[i]; r.X.X == x && r.X.Y == y && r.X.W == w && r.X.H == h {
			return r.Layout.X, r.Layout.Y, true
		}
	}
	return 0, 0, false
}

// ToX converts a layout point to X using the shared layout origin.
func (s *Space) ToX(lx, ly int) (x, y int) {
	if s.n == 0 {
		return lx, ly
	}
	return lx - s.ox, ly - s.oy
}

// ToLayout converts an X point to layout coordinates.
func (s *Space) ToLayout(x, y int) (lx, ly int) {
	if s.n == 0 {
		return x, y
	}
	return x + s.ox, y + s.oy
}
