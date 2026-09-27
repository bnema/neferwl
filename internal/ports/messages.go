package ports

import (
	"image"
	"os"
	"time"
)

// CaptureRequest transfers ownership of Dst.File to the output goroutine.
type CaptureRequest struct {
	ID                    uint64
	Output                string
	Region                image.Rectangle // clipped output buffer coordinates
	Cursor                bool
	Dst                   SHMBuffer
	Width, Height, Stride int
	Format                uint32
}

// CaptureDone is attempted once after the output closes the destination.
// Time holds CLOCK_MONOTONIC seconds and nanoseconds since boot, not wall time.
type CaptureDone struct {
	ID     uint64
	Output string
	Err    error
	Time   time.Time
}

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

// WindowFullscreenRequest carries wayland → core fullscreen requests.
type WindowFullscreenRequest struct {
	ID         WindowID
	Fullscreen bool
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
// position, where input holds a locked pointer.
type PointerConstraint struct {
	Mode ConstraintMode
	Rect Rect
	X, Y float64
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

// Mods carries input → core modifier flags.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
)

// InputEvent carries input → core notifications.
type InputEvent interface{ inputEvent() }

// KeyEvent carries input → core key transitions; Keysym is an xkb keysym name.
// Keycode is the evdev code (xkb keycode - 8) and State the xkb modifier state
// after this transition; wayland forwards both to clients unchanged.
type KeyEvent struct {
	Keysym string
	// Base is the key's unshifted keysym in the active layout, so Cmd+Shift+1
	// still matches a cmd+shift+1 bind although it prints exclam.
	Base     string
	Mods     Mods
	Pressed  bool
	TimeMsec uint32
	Keycode  uint32
	State    ModState
}

// ModState is the serialized xkb modifier state sent in wl_keyboard.modifiers.
type ModState struct{ Depressed, Latched, Locked, Group uint32 }

func (KeyEvent) inputEvent() {}

// PointerMotion uses global layout coordinates in logical pixels (see OutputPlacement).
// DX and DY are the accelerated logical deltas and UnaccelDX, UnaccelDY the
// raw device deltas; all four are 0 for absolute devices. TimeUsec is the
// device timestamp in microseconds.
type PointerMotion struct {
	X, Y                 float64
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
	TimeMsec             uint32
	TimeUsec             uint64
}

func (PointerMotion) inputEvent() {}

// PointerButton uses evdev button codes (BTN_LEFT is 0x110).
type PointerButton struct {
	Button   uint32
	Pressed  bool
	TimeMsec uint32
}

func (PointerButton) inputEvent() {}

// AxisSource tells how a scroll was made; values match wl_pointer.axis_source.
type AxisSource uint8

const (
	AxisWheel AxisSource = iota
	AxisFinger
	AxisContinuous
)

// ScrollAxis is one scroll direction in a pointer frame. Value is in
// surface pixels; V120 counts wheel detents in 1/120 steps (wheel only).
// Stop ends a finger or continuous scroll on this axis.
type ScrollAxis struct {
	Set   bool
	Value float64
	V120  int32
	Stop  bool
}

// PointerAxis is one scroll frame: Vertical, then Horizontal.
type PointerAxis struct {
	Source               AxisSource
	Vertical, Horizontal ScrollAxis
	TimeMsec             uint32
}

func (PointerAxis) inputEvent() {}

// OutputEvent carries output → core notifications.
type OutputEvent interface{ outputEvent() }

// OutputInfo describes a connected display. Sizes are physical pixels.
type OutputInfo struct {
	// Name is the connector (e.g. DP-2), matched against output.<name>.* config.
	Name string
	// Make, Model and Serial come from EDID; empty when unknown.
	Make, Model, Serial  string
	Width, Height        int
	RefreshMilli         int
	PhysicalW, PhysicalH int // millimetres
}

// Key identifies the monitor across connectors: make, model and serial, or
// the connector name when EDID has no serial.
func (i OutputInfo) Key() string {
	if i.Serial == "" {
		return i.Name
	}
	return i.Make + " " + i.Model + " " + i.Serial
}

// OutputAdded carries output → core a new display, or a new mode for a known
// connector.
type OutputAdded struct{ Info OutputInfo }

func (OutputAdded) outputEvent() {}

// OutputPresented carries output → wayland what an output shows and reads.
// Flip is set when a page flip completed: frame callbacks of the surfaces
// on it are due. Shown and Queued are the DMABuf IDs scanned out directly
// (0: a composed image); Seen is the latest content Seq per window the
// output finished reading (the GPU is done with it). A replaced client
// buffer is released once every output that reports has seen a later
// content of its window and neither shows nor queues it.
type OutputPresented struct {
	Output        string
	Flip          *FlipInfo
	Shown, Queued uint64
	Seen          map[WindowID]uint64
}

// OutputFormats carries output → wayland the dmabuf formats an output can
// scan out directly (its primary plane's, that the renderer also samples,
// so a refused buffer can still be composed). Device is the KMS device
// (dev_t) clients allocate scanout buffers for. Sent at start and after
// every modeset; empty Formats means no direct scanout.
type OutputFormats struct {
	Output  string
	Device  uint64
	Formats []DMABufFormat
}

// FlipInfo is one completed page flip. When is its CLOCK_MONOTONIC time
// (hardware clock on DRM), Seq the output's vblank counter, Refresh the
// refresh period (0 while variable refresh is on). ZeroCopy is the window
// whose buffer was shown without composition (direct scanout or overlay
// plane; 0: none), Async set for a tearing flip.
// Shows is the content Seq per window the flipped frame shows. Merged
// counts earlier flips folded into this one when the reader fell behind:
// their presentation feedback is discarded.
type FlipInfo struct {
	When          time.Duration
	Seq           uint64
	Refresh       time.Duration
	ZeroCopy      WindowID
	Async         bool
	HardwareClock bool
	Merged        int
	Shows         map[WindowID]uint64
}

// OutputRemoved carries output → core an unplugged display.
type OutputRemoved struct{ Name string }

func (OutputRemoved) outputEvent() {}

// ConfigChanged carries config → core reloads.
type ConfigChanged struct{ Config Config }

// ClientCommand carries core → wayland commands.
type ClientCommand interface{ clientCommand() }

// ShortcutsInhibitState carries core → wayland whether a window's
// shortcuts inhibitor is in effect (it has keyboard focus).
type ShortcutsInhibitState struct {
	Window WindowID
	Active bool
}

func (ShortcutsInhibitState) clientCommand() {}

// UserActivity carries core → wayland that the user touched an input
// device, for idle notifications (ext_idle_notifier_v1). Core sends at
// most one per ActivityInterval.
type UserActivity struct{}

func (UserActivity) clientCommand() {}

// ActivityInterval is the most often core reports UserActivity: idle
// timers may start up to this much early, so no idle timeout is shorter.
const ActivityInterval = 100 * time.Millisecond

// ConfigureWindow carries core → wayland geometry and state. Output is the
// connector showing the window, empty while it is hidden.
type ConfigureWindow struct {
	ID                    WindowID
	Width, Height         int
	Fullscreen, Activated bool
	// Floating windows are not tiled: no tiled states.
	Floating bool
	Output   string
}

func (ConfigureWindow) clientCommand() {}

// OutputPlacement is one output in the global layout. X, Y, Width and
// Height are logical; physical = logical × Scale.
type OutputPlacement struct {
	Info                OutputInfo
	X, Y, Width, Height int
	Scale               float64
	// Primary is output.<name>.primary: the pointer starts on it.
	Primary bool
}

// Contains reports whether the logical point is on the output.
func (o OutputPlacement) Contains(x, y float64) bool {
	return x >= float64(o.X) && x < float64(o.X+o.Width) && y >= float64(o.Y) && y < float64(o.Y+o.Height)
}

// Layout is the global arrangement of outputs, left to right.
type Layout []OutputPlacement

// At returns the output under the logical point.
func (l Layout) At(x, y float64) (OutputPlacement, bool) {
	for _, o := range l {
		if o.Contains(x, y) {
			return o, true
		}
	}
	return OutputPlacement{}, false
}

// Clamp keeps the pointer on an output: a point outside every output stays
// on the output under (fromX, fromY), or the first one, clamped to its edges.
func (l Layout) Clamp(fromX, fromY, x, y float64) (float64, float64) {
	if len(l) == 0 {
		return x, y
	}
	if _, ok := l.At(x, y); ok {
		return x, y
	}
	o, ok := l.At(fromX, fromY)
	if !ok {
		o = l[0]
	}
	x = min(max(x, float64(o.X)), float64(o.X+o.Width)-1)
	y = min(max(y, float64(o.Y)), float64(o.Y+o.Height)-1)
	return x, y
}

// SetOutputs carries core → wayland every output and the focused one (where
// new surfaces start). It is sent when the list, a placement or the focus changes.
type SetOutputs struct {
	Outputs Layout
	Focused string
	// Off lists the outputs a client turned off (output power management),
	// in Outputs order.
	Off []string
}

func (SetOutputs) clientCommand() {}

// SlotsPending tells wayland whether a slot waits for its window. Only then
// does it read the SlotEnv of mapping clients.
type SlotsPending struct{ Pending bool }

func (SlotsPending) clientCommand() {}

// CloseWindow carries core → wayland close requests.
type CloseWindow struct{ ID WindowID }

func (CloseWindow) clientCommand() {}

// FocusWindow carries core → wayland keyboard focus (0 means none).
type FocusWindow struct{ ID WindowID }

func (FocusWindow) clientCommand() {}

// ConfigurePopup carries core → wayland a popup's place, relative to its
// parent's window geometry. Reposition answers a reposition request.
type ConfigurePopup struct {
	ID         WindowID
	Rect       Rect
	Reposition bool
	Token      uint32
}

func (ConfigurePopup) clientCommand() {}

// ClosePopup carries core → wayland a popup to dismiss (popup_done).
type ClosePopup struct{ ID WindowID }

func (ClosePopup) clientCommand() {}

// PointerFocus changes the pointer surface; ID 0 clears focus. Coordinates are surface-local.
type PointerFocus struct {
	ID   WindowID
	X, Y float64
}

func (PointerFocus) clientCommand() {}

// PointerMotionTo carries core → wayland motion for the pointer focus, in
// window coordinates. The deltas and TimeUsec feed zwp_relative_pointer_v1;
// While the window locks the pointer, wayland sends only relative motion.
type PointerMotionTo struct {
	ID                   WindowID
	X, Y                 float64
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
	TimeMsec             uint32
	TimeUsec             uint64
}

func (PointerMotionTo) clientCommand() {}

type PointerButtonTo struct {
	ID       WindowID
	Button   uint32
	Pressed  bool
	TimeMsec uint32
}

func (PointerButtonTo) clientCommand() {}

// PointerAxisTo sends a scroll frame to a window.
type PointerAxisTo struct {
	ID   WindowID
	Axis PointerAxis
}

func (PointerAxisTo) clientCommand() {}

// ForwardKey carries core → wayland unbound keys.
type ForwardKey struct {
	ID  WindowID
	Key KeyEvent
}

func (ForwardKey) clientCommand() {}

// SetKeymap carries app → wayland repeat info and, unless Keymap is empty, a new
// xkb keymap in text format.
type SetKeymap struct {
	Keymap                  string
	RepeatRate, RepeatDelay int
}

func (SetKeymap) clientCommand() {}

// CursorImage is a cursor frame in premultiplied B8G8R8A8, W*4 bytes per
// row; (HotX, HotY) is the click point. Sizes are physical pixels.
type CursorImage struct {
	W, H, HotX, HotY int
	Pixels           []byte
}

// CursorChange carries wayland → outputs the cursor the focused client asks
// for: a CSS cursor name (wp_cursor_shape_v1), its own image, or none.
// The zero value is the default arrow.
type CursorChange struct {
	// Shape is a CSS cursor name such as "pointer" or "text".
	Shape string
	// Image is a client cursor at Scale buffer pixels per logical pixel.
	Image  *CursorImage
	Scale  int
	Hidden bool
}

// SpawnRequest carries core → launcher process arguments.
type SpawnRequest struct {
	Argv []string
	// Env adds KEY=value entries to the child environment.
	Env []string
}

// Scene carries core → renderer immutable snapshots with fresh Windows slices,
// one per output. Rects and the output size are logical and local to the
// output; the renderer multiplies by Scale.
type Scene struct {
	Output                    string
	Seq                       uint64
	OutputWidth, OutputHeight int
	Scale                     float64
	// Off turns the display off (output power management): nothing is
	// drawn until a scene without it.
	Off        bool
	Background string
	Border     Border
	Windows    []SceneWindow
	// Separators are the lines between windows, drawn in slice order with
	// the Border colors: tile lines over the tiles, under the floats; a
	// float's border right after the float.
	Separators []Separator
	// Layers are drawn in slice order: background and bottom before windows,
	// top and overlay after. A fullscreen window covers bottom and top.
	// Window popups are drawn after the windows, layer popups
	// (SceneWindow.OverLayers) last, over every layer.
	Layers []SceneLayer
}

// Shows reports whether the scene draws the surface of id: only its
// content changes need a new frame.
func (s Scene) Shows(id WindowID) bool {
	for _, w := range s.Windows {
		if w.ID == id && !w.Hidden {
			return true
		}
	}
	for _, l := range s.Layers {
		if l.ID == id {
			return true
		}
	}
	return false
}

// Border carries the window border style; colors are #rrggbb, "" skips
// drawing. Width is in logical pixels.
type Border struct {
	Width            int
	Active, Inactive string
}

// Separator is a line between windows, in logical pixels. Active lines
// mark the focused window and use Border.Active, others Border.Inactive.
// Window is the float whose border it is; 0 for lines between tiles.
type Separator struct {
	Rect   Rect
	Active bool
	Window WindowID
}

// SceneLayer carries core → renderer layer surface placement.
type SceneLayer struct {
	ID    WindowID
	Layer Layer
	Rect  Rect
}

// SurfaceContent carries wayland → output the latest committed content of a
// window: SHM, a client shared-memory buffer, or DMABuf, a GPU buffer.
// Renderers read both in place and never modify them. Empty means the window
// has no content. LogicalW and LogicalH are the surface size in logical
// pixels (buffer scale and viewport applied). Subsurfaces come in Children;
// Geometry is the part of the surface that is the window (xdg window
// geometry), the rest being client shadows.
type SurfaceContent struct {
	ID WindowID
	// Seq counts the window's contents (wayland sets it), for release.
	Seq                uint64
	Width, Height      int
	LogicalW, LogicalH int
	Opaque             bool // x formats: ignore the alpha byte
	SHM                *SHMBuffer
	DMABuf             *DMABuf
	// Children are the subsurfaces, bottom to top, flattened.
	Children []Subsurface
	// Geometry is in logical pixels from the surface origin; empty means
	// the whole surface.
	Geometry Rect
	// Async asks for tearing presentation (wp_tearing_control_v1); outputs
	// honour it only in direct scanout.
	Async bool
	// Acquire is the explicit-sync fence of this content's root buffer
	// (wp_linux_drm_syncobj_v1): readers wait on it before reading the
	// buffer, instead of the buffer's implicit fences. Wayland owns it and
	// closes it when the buffer is released; a reader that keeps it past
	// the call duplicates it under Acquire.SyscallConn. nil: implicit sync.
	// Wayland closes it only after every output reported a later content of
	// the window as seen, so a reader never gets it already closed.
	Acquire *os.File
	// DamageHistory is what changed in the root surface's buffer over the
	// window's last contents, oldest first, ending with this one (Seq).
	// A renderer holding an older content redraws the union of the
	// entries after it, or everything when the history does not reach it.
	DamageHistory []SeqDamage
}

// SeqDamage is what content Seq changed from the one before: Rects in
// root buffer pixels, or everything when Full.
type SeqDamage struct {
	Seq   uint64
	Full  bool
	Rects []Rect
}

// DamageSince is the union of changes after content seq up to this one,
// and false when the history does not reach back to seq (redraw all).
func (c SurfaceContent) DamageSince(seq uint64) ([]Rect, bool) {
	if seq >= c.Seq {
		return nil, true
	}
	h := c.DamageHistory
	if len(h) == 0 || h[0].Seq > seq+1 || h[len(h)-1].Seq != c.Seq {
		return nil, false
	}
	var out []Rect
	for _, d := range h {
		if d.Seq <= seq {
			continue
		}
		if d.Full {
			return nil, false
		}
		out = append(out, d.Rects...)
	}
	return out, true
}

// Subsurface is a child surface at X, Y logical pixels from the root
// surface origin. Below children are drawn under the root surface.
type Subsurface struct {
	X, Y  int
	Below bool
	SurfaceContent
}

// Empty reports whether the content has nothing to draw.
func (c SurfaceContent) Empty() bool {
	return c.SHM == nil && c.DMABuf == nil && len(c.Children) == 0
}

// SHMBuffer is a client wl_shm buffer: B8G8R8A8 pixels (argb8888/xrgb8888
// little-endian) at Offset in the pool File, Stride bytes per row. The
// client keeps writing the file, so renderers copy from it when they draw.
// Pool is unique per wl_shm pool for the session: renderers map a pool
// once. File stays open while the client's buffer exists and follows the
// DMABuf rules: duplicate it under File.SyscallConn before keeping it. A
// client may shrink the file at any time, so readers must survive faults.
type SHMBuffer struct {
	Pool           uint64
	File           *os.File
	Offset, Stride int
}

// DMABuf is a client GPU buffer (linux-dmabuf). Its files stay open while
// the client's buffer exists; a renderer that keeps it must duplicate the
// descriptors under File.SyscallConn so a concurrent close cannot hand it a
// reused one. ID is unique per buffer for the whole session: renderers key
// their imports on it.
type DMABuf struct {
	ID            uint64
	Width, Height int
	Format        uint32 // DRM fourcc
	Modifier      uint64
	Planes        []DMABufPlane
}

// DMABufPlane is one plane of a DMABuf.
type DMABufPlane struct {
	File           *os.File
	Offset, Stride uint32
}

// DMABufFormat is a DRM fourcc with a modifier the renderer can import.
type DMABufFormat struct {
	Format   uint32
	Modifier uint64
}

// DMABufSupport is what the renderer imports: the render device (a dev_t,
// 0 when unknown) and the formats. No formats means no dmabuf.
type DMABufSupport struct {
	Device  uint64
	Formats []DMABufFormat
}

// SceneWindow carries core → renderer window placement.
type SceneWindow struct {
	ID                          WindowID
	Rect                        Rect
	Focused, Fullscreen, Hidden bool
	// Inset sides carry a separator line inside Rect: the client is drawn
	// inside it.
	Inset Sides
	// Floating windows are drawn over the tiles and their lines.
	Floating bool
	// Popups are drawn from their content only: no border, no background.
	Popup bool
	// OverLayers popups hang from a layer surface: drawn over the top and
	// overlay layers. Window popups stay with the windows, under them.
	OverLayers bool
}

// State carries core → state publisher a snapshot of what is on screen, for
// scripts (bars, status lines). Workspace numbers count from 1.
type State struct {
	// Output is the focused output; "" before the first one.
	Output  string
	Outputs []OutputState
	// Window is the focused window, nil when none.
	Window  *WindowState
	Windows []WindowState
}

// OutputState is one output: its workspace on screen and how many numbered
// workspaces it has. Active is 0 while a hidden (named) workspace is shown.
type OutputState struct {
	Name      string
	Active    int
	Count     int
	Workspace string // name of the workspace on screen, "" if unnamed
}

// WindowState is one mapped window and where it is.
type WindowState struct {
	ID     WindowID
	AppID  string
	PID    int
	Output string
	// Workspace is the window's numbered workspace, 0 for a hidden one.
	Workspace int
	// Visible means on the workspace on screen (it may be behind a
	// fullscreen window).
	Visible bool
	// IdleInhibit is set while the window keeps the session from idling.
	IdleInhibit bool `json:",omitempty"`
}
