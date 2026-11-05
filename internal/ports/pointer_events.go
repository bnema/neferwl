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

// VirtualPointerInput carries wayland → core one event of a virtual pointer
// (zwlr_virtual_pointer_v1: wlrctl, agents). Event is a PointerMotion,
// PointerButton or PointerAxis whose Device names the virtual pointer.
// Core places the pointer: a relative PointerMotion carries only DX, DY
// (X, Y unset); an Absolute one carries FX, FY in [0, 1] of Output's
// rectangle, or of the whole layout when Output is empty. Core then routes
// it as real input, so focus, hover and grabs follow the same decisions.
// State is the security gate when the request arrived: core drops events
// of another epoch or of a protected session.
type VirtualPointerInput struct {
	State    domain.SecurityState
	Event    InputEvent
	Absolute bool
	FX, FY   float64
	Output   string
}

func (VirtualPointerInput) clientEvent() {}
