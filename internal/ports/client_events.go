package ports

import "github.com/bnema/neferwl/internal/domain"

// ClientEvent carries wayland → core notifications.
type ClientEvent interface{ clientEvent() }

// WindowMapped carries wayland → core mapping.
type WindowMapped struct {
	ID    domain.WindowID
	AppID string
	// Slot is the SlotEnv value of the client process, empty if none.
	Slot string
	// PID is the client process, 0 when unknown.
	PID int
	// PIDShared means PID identifies a bridge process, not the application.
	PIDShared bool
	// Sandbox is the wp_security_context_v1 app id of the client, else
	// its engine, "" when not sandboxed.
	Sandbox string
	// ContentType is the root surface's committed content hint at map time.
	ContentType domain.ContentType
	// ColorDescription reports a committed root transfer function or primaries.
	ColorDescription bool
	// Xwayland is set for an X11 window, with either backend.
	Xwayland bool
	// Fullscreen is the last fullscreen state requested before the first map
	// (xdg set_fullscreen, X11 _NET_WM_STATE_FULLSCREEN).
	Fullscreen bool
	// Floating windows are dialogs (a parent) or fixed-size windows such
	// as splash screens: core shows them over the columns at their own
	// size, Width×Height logical (0 when unknown).
	Floating      bool
	Width, Height int
	// Parent is the window a dialog belongs to (xdg_toplevel.set_parent or
	// xdg-foreign), 0 when none. A dialog of a fullscreen window shows over
	// it.
	Parent domain.WindowID
	// Positioned windows are placed by core: it sends PlaceWindow with the
	// client rect origin. Set only by the X11 host, false for xdg.
	Positioned bool
	// Remap is set when the toplevel mapped before and was unmapped (a tray
	// app restored): window rules only place its first map.
	Remap bool
	// Modal is set for a dialog that blocks its parent (xdg_dialog_v1):
	// focusing the parent focuses the dialog.
	Modal bool
	// HasAt asks for the screen containing the layout point (AtX, AtY) when
	// no rule sets a monitor: the X11 host sets it for a window that maps at
	// exactly one output's rect.
	HasAt    bool
	AtX, AtY int
}

// SlotEnv is the environment variable neferwl sets on processes it spawns
// for a slot. The client's value maps its windows to the slot.
const SlotEnv = "NEFERWL_SLOT"

func (WindowMapped) clientEvent() {}

// InputRegionChanged replaces the surface-local hit area for a mapped surface.
// All means the full hit rectangle; otherwise Rects is the exact union.
type InputRegionChanged struct {
	ID    domain.WindowID
	All   bool
	Rects []domain.Rect
}

func (InputRegionChanged) clientEvent() {}

// WindowUnmapped carries wayland → core removal.
type WindowUnmapped struct{ ID domain.WindowID }

func (WindowUnmapped) clientEvent() {}

// UnmanagedMapped carries wayland → core that an override-redirect X11 window
// shows. The rect is its place in the layout (logical coordinates). NoInput
// windows (tooltips, drag icons, notifications) never take the pointer.
type UnmanagedMapped struct {
	ID                  domain.WindowID
	X, Y, Width, Height int
	NoInput             bool
}

func (UnmanagedMapped) clientEvent() {}

// UnmanagedMoved carries wayland → core a move and/or resize of a mapped
// unmanaged window.
type UnmanagedMoved struct {
	ID                  domain.WindowID
	X, Y, Width, Height int
}

func (UnmanagedMoved) clientEvent() {}

// UnmanagedUnmapped carries wayland → core that an unmanaged window is gone.
type UnmanagedUnmapped struct{ ID domain.WindowID }

func (UnmanagedUnmapped) clientEvent() {}

// ShortcutsInhibit carries wayland → core that a window asks for (Active)
// or stops asking for all keys, compositor binds included, while it has
// keyboard focus (zwp_keyboard_shortcuts_inhibit_v1).
type ShortcutsInhibit struct {
	Window domain.WindowID
	Active bool
}

func (ShortcutsInhibit) clientEvent() {}

// IdleInhibit carries wayland → core that a window keeps (Active) or
// stops keeping the session from going idle (zwp_idle_inhibit_v1).
type IdleInhibit struct {
	Window domain.WindowID
	Active bool
}

func (IdleInhibit) clientEvent() {}

// OutputPower carries wayland → core a client turning a display off or
// back on (zwlr_output_power_v1).
type OutputPower struct {
	Output string
	On     bool
}

func (OutputPower) clientEvent() {}

// WindowMoveRequest carries wayland → core xdg_toplevel move and resize
// requests answering the client's current press: core starts a pointer
// drag of the window with the held button.
type WindowMoveRequest struct {
	ID     domain.WindowID
	Resize bool
	Edges  domain.ResizeEdges
}

func (WindowMoveRequest) clientEvent() {}

// WindowFullscreenRequest carries wayland → core fullscreen requests.
// External is set when another client (a taskbar, through foreign
// toplevel) asks for it, not the window itself.
type WindowFullscreenRequest struct {
	ID         domain.WindowID
	Fullscreen bool
	External   bool
}

func (WindowFullscreenRequest) clientEvent() {}

// WindowActivate carries wayland → core a valid xdg-activation request:
// show the window's workspace and focus it.
type WindowActivate struct{ ID domain.WindowID }

func (WindowActivate) clientEvent() {}

// WindowAppID carries wayland → core an app ID set after the window mapped.
type WindowAppID struct {
	ID    domain.WindowID
	AppID string
}

func (WindowAppID) clientEvent() {}

// WindowContentType carries wayland → core a content hint changed after map.
type WindowContentType struct {
	ID   domain.WindowID
	Type domain.ContentType
}

func (WindowContentType) clientEvent() {}

// WindowParent carries the new parent of a mapped dialog (0: none), after
// set_parent or an xdg-foreign import changed or ended the relationship.
type WindowParent struct {
	ID, Parent domain.WindowID
}

func (WindowParent) clientEvent() {}

// WindowModal carries a mapped dialog's new modal state (xdg_dialog_v1).
type WindowModal struct {
	ID    domain.WindowID
	Modal bool
}

func (WindowModal) clientEvent() {}

// WindowResized carries wayland → core the new size of a floating window.
type WindowResized struct {
	ID            domain.WindowID
	Width, Height int
}

func (WindowResized) clientEvent() {}

// WindowColorDescription reports the root surface's committed colour fact.
// Subsurface descriptions do not change the window's eligibility.
type WindowColorDescription struct {
	ID       domain.WindowID
	PlainSDR bool
}

func (WindowColorDescription) clientEvent() {}
