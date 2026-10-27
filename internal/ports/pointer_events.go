package ports

import "github.com/bnema/neferwl/internal/domain"

// PointerConstrained carries wayland → core the constraint active on a
// window (zwp_pointer_constraints_v1); ID 0 means none is active. A
// constraint is active only on the window with both pointer and keyboard
// focus.
type PointerConstrained struct {
	ID domain.WindowID
	domain.PointerConstraint
}

func (PointerConstrained) clientEvent() {}

// PointerWarp carries wayland → core a client's request to move the
// pointer to X, Y, window-local logical (wp_pointer_warp_v1). Wayland has
// checked that the window has the pointer and the point is on it.
type PointerWarp struct {
	ID   domain.WindowID
	X, Y float64
}

func (PointerWarp) clientEvent() {}
