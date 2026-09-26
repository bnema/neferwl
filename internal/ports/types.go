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
