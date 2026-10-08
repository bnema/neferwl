package ports

// WindowID identifies a window.
type WindowID uint64

// Rect is a pixel rectangle.
type Rect struct{ X, Y, W, H int }

// Sides is a set of window sides.
type Sides uint8

const (
	SideLeft Sides = 1 << iota
	SideRight
	SideTop
	SideBottom
	SideAll = SideLeft | SideRight | SideTop | SideBottom
)

// Overlaps reports whether r and o share a positive area: touching edges
// do not overlap.
func (r Rect) Overlaps(o Rect) bool {
	return r.W > 0 && r.H > 0 && o.W > 0 && o.H > 0 &&
		r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H
}

// Contains reports whether o lies wholly inside r.
func (r Rect) Contains(o Rect) bool {
	return o.X >= r.X && o.Y >= r.Y && o.X+o.W <= r.X+r.W && o.Y+o.H <= r.Y+r.H
}

// Insets are distances from the four edges of an area.
type Insets struct{ Top, Right, Bottom, Left int }

// Inset shrinks r by b on each side in s.
func (r Rect) Inset(s Sides, b int) Rect {
	b = min(max(b, 0), r.W/2, r.H/2)
	if s&SideLeft != 0 {
		r.X, r.W = r.X+b, r.W-b
	}
	if s&SideRight != 0 {
		r.W -= b
	}
	if s&SideTop != 0 {
		r.Y, r.H = r.Y+b, r.H-b
	}
	if s&SideBottom != 0 {
		r.H -= b
	}
	return r
}
