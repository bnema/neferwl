package ports

// ConstraintMode is how a pointer constraint holds the pointer.
type ConstraintMode uint8

const (
	ConstraintNone ConstraintMode = iota
	// ConstraintLock keeps the pointer still: only relative motion flows.
	ConstraintLock
	// ConstraintConfine keeps the pointer inside Rect.
	ConstraintConfine
)

// PointerConstraint is an active pointer constraint. Rect is logical: in
// PointerConstrained it is window-local and empty means the whole window;
// core sends input the resolved global rectangle, and in X, Y its cursor
// position, where input holds a locked pointer. Warp moves input's
// pointer to X, Y whatever the mode (wp_pointer_warp_v1).
type PointerConstraint struct {
	Mode ConstraintMode
	Rect Rect
	X, Y float64
	Warp bool
}

// Clamp keeps a global point inside the rectangle of a lock or confine;
// no constraint and an empty rectangle leave it unchanged.
func (c PointerConstraint) Clamp(x, y float64) (float64, float64) {
	if c.Mode == ConstraintNone || c.Rect.W <= 0 || c.Rect.H <= 0 {
		return x, y
	}
	x = min(max(x, float64(c.Rect.X)), float64(c.Rect.X+c.Rect.W-1))
	y = min(max(y, float64(c.Rect.Y)), float64(c.Rect.Y+c.Rect.H-1))
	return x, y
}

// PointerConstrained carries wayland → core the constraint active on a
// window (zwp_pointer_constraints_v1); ID 0 means none is active. A
// constraint is active only on the window with both pointer and keyboard
// focus.
type PointerConstrained struct {
	ID WindowID
	PointerConstraint
}

func (PointerConstrained) clientEvent() {}

// PointerWarp carries wayland → core a client's request to move the
// pointer to X, Y, window-local logical (wp_pointer_warp_v1). Wayland has
// checked that the window has the pointer and the point is on it.
type PointerWarp struct {
	ID   WindowID
	X, Y float64
}

func (PointerWarp) clientEvent() {}
