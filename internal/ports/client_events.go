package ports

// ClientEvent carries wayland → core notifications.
type ClientEvent interface{ clientEvent() }

// WindowMapped carries wayland → core mapping.
type WindowMapped struct {
	ID    WindowID
	AppID string
	// Slot is the SlotEnv value of the client process, empty if none.
	Slot string
	// PID is the client process, 0 when unknown.
	PID int
	// Floating windows are dialogs (a parent) or fixed-size windows such
	// as splash screens: core shows them over the columns at their own
	// size, Width×Height logical (0 when unknown).
	Floating      bool
	Width, Height int
	// Parent is the window a dialog belongs to (xdg_toplevel.set_parent or
	// xdg-foreign), 0 when none. A dialog of a fullscreen window shows over
	// it.
	Parent WindowID
}

// SlotEnv is the environment variable neferwl sets on processes it spawns
// for a slot. The client's value maps its windows to the slot.
const SlotEnv = "NEFERWL_SLOT"

func (WindowMapped) clientEvent() {}

// InputRegionChanged replaces the surface-local hit area for a mapped surface.
// All means the full hit rectangle; otherwise Rects is the exact union.
type InputRegionChanged struct {
	ID    WindowID
	All   bool
	Rects []Rect
}

func (InputRegionChanged) clientEvent() {}

// WindowUnmapped carries wayland → core removal.
type WindowUnmapped struct{ ID WindowID }

func (WindowUnmapped) clientEvent() {}

// ShortcutsInhibit carries wayland → core that a window asks for (Active)
// or stops asking for all keys, compositor binds included, while it has
// keyboard focus (zwp_keyboard_shortcuts_inhibit_v1).
type ShortcutsInhibit struct {
	Window WindowID
	Active bool
}

func (ShortcutsInhibit) clientEvent() {}

// IdleInhibit carries wayland → core that a window keeps (Active) or
// stops keeping the session from going idle (zwp_idle_inhibit_v1).
type IdleInhibit struct {
	Window WindowID
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

// ResizeEdges are the window edges a pointer resize moves, as bit flags
// (the xdg_toplevel resize_edge values).
type ResizeEdges uint32

const (
	ResizeTop    ResizeEdges = 1
	ResizeBottom ResizeEdges = 2
	ResizeLeft   ResizeEdges = 4
	ResizeRight  ResizeEdges = 8
)

// WindowMoveRequest carries wayland → core xdg_toplevel move and resize
// requests answering the client's current press: core starts a pointer
// drag of the window with the held button.
type WindowMoveRequest struct {
	ID     WindowID
	Resize bool
	Edges  ResizeEdges
}

func (WindowMoveRequest) clientEvent() {}

// WindowFullscreenRequest carries wayland → core fullscreen requests.
// External is set when another client (a taskbar, through foreign
// toplevel) asks for it, not the window itself.
type WindowFullscreenRequest struct {
	ID         WindowID
	Fullscreen bool
	External   bool
}

func (WindowFullscreenRequest) clientEvent() {}

// WindowActivate carries wayland → core a valid xdg-activation request:
// show the window's workspace and focus it.
type WindowActivate struct{ ID WindowID }

func (WindowActivate) clientEvent() {}

// WindowAppID carries wayland → core an app ID set after the window mapped.
type WindowAppID struct {
	ID    WindowID
	AppID string
}

func (WindowAppID) clientEvent() {}

// WindowParent carries the new parent of a mapped dialog (0: none), after
// set_parent or an xdg-foreign import changed or ended the relationship.
type WindowParent struct {
	ID, Parent WindowID
}

func (WindowParent) clientEvent() {}

// WindowResized carries wayland → core the new size of a floating window.
type WindowResized struct {
	ID            WindowID
	Width, Height int
}

func (WindowResized) clientEvent() {}
