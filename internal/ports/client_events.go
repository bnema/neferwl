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

// WorkspaceActivate requests showing workspaces by stable ID, in commit order.
// Core applies the entire batch before publishing a new state.
type WorkspaceActivate struct{ IDs []uint64 }

func (WorkspaceActivate) clientEvent() {}

// Workspaces is an immutable, latest-only inventory of output workspaces.
type Workspaces struct{ Outputs []WorkspaceOutput }
type WorkspaceOutput struct {
	Name       string
	Workspaces []WorkspaceInfo
}
type WorkspaceInfo struct {
	ID             uint64
	Configured     string // configured name; empty for dynamic workspaces. ID, not this, identifies the workspace
	Name           string
	Index          int // zero-based vertical coordinate
	Active, Hidden bool
	// Frame is the workspace viewport in output-local logical pixels
	// (Monitor.Frame geometry: the whole output unless the workspace has a
	// size override). It is where a capture session of it lies.
	Frame Rect
}

// WindowAppID carries wayland → core an app ID set after the window mapped.
type WindowAppID struct {
	ID    WindowID
	AppID string
}

func (WindowAppID) clientEvent() {}

// WindowResized carries wayland → core the new size of a floating window.
type WindowResized struct {
	ID            WindowID
	Width, Height int
}

func (WindowResized) clientEvent() {}

// Popup anchors and gravities, as xdg_positioner values.
const (
	EdgeNone uint32 = iota
	EdgeTop
	EdgeBottom
	EdgeLeft
	EdgeRight
	EdgeTopLeft
	EdgeBottomLeft
	EdgeTopRight
	EdgeBottomRight
)

// Popup constraint adjustments, as xdg_positioner values.
const (
	AdjustSlideX uint32 = 1 << iota
	AdjustSlideY
	AdjustFlipX
	AdjustFlipY
	AdjustResizeX
	AdjustResizeY
)

// Positioner places a popup relative to its parent's window geometry:
// the Anchor point of AnchorRect, moved by Offset, with the popup extending
// towards Gravity. Adjust says how core may move it to stay on screen.
type Positioner struct {
	Width, Height    int
	AnchorRect       Rect
	Anchor, Gravity  uint32
	OffsetX, OffsetY int
	Adjust           uint32
}

// PopupRequest carries wayland → core a popup to place (xdg_popup), on
// its first commit and on reposition (Reposition, with the client's
// Token). Parent is a window or popup. Grab popups take the keyboard and
// close on a click elsewhere.
type PopupRequest struct {
	ID, Parent WindowID
	Positioner Positioner
	Grab       bool
	Reposition bool
	Token      uint32
}

func (PopupRequest) clientEvent() {}

// PopupMapped carries wayland → core a placed popup that now has content.
// It ends with WindowUnmapped.
type PopupMapped struct{ ID WindowID }

func (PopupMapped) clientEvent() {}

// Layer is a wlr-layer-shell stacking layer.
type Layer uint32

const (
	LayerBackground Layer = iota
	LayerBottom
	LayerTop
	LayerOverlay
)

// Anchor edges, as in zwlr_layer_surface_v1.anchor.
const (
	AnchorTop    uint32 = 1
	AnchorBottom uint32 = 2
	AnchorLeft   uint32 = 4
	AnchorRight  uint32 = 8
)

// LayerSurface is the committed state of one mapped layer surface. Width and
// Height are the committed surface size in logical pixels. Margin is top, right, bottom, left.
// IDs share the WindowID space with windows and never collide.
type LayerSurface struct {
	ID            WindowID
	Layer         Layer
	Anchor        uint32
	ExclusiveZone int32
	Margin        [4]int32
	Width, Height int
	Namespace     string
	// Keyboard is the requested interactivity: 0 none, 1 exclusive, 2 on-demand.
	Keyboard uint32
	// Output is the connector the surface is on.
	Output string
}

// LayerChanged carries wayland → core the full list of mapped layer surfaces,
// sent whenever a layer surface maps, unmaps or commits new layer state.
type LayerChanged struct{ Layers []LayerSurface }

func (LayerChanged) clientEvent() {}

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
