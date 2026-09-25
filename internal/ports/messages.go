package ports

import "os"

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
}

// SlotEnv is the environment variable nefertty sets on processes it spawns
// for a slot. The client's value maps its windows to the slot.
const SlotEnv = "NEFERTTY_SLOT"

func (WindowMapped) clientEvent() {}

// WindowUnmapped carries wayland → core removal.
type WindowUnmapped struct{ ID WindowID }

func (WindowUnmapped) clientEvent() {}

// WindowFullscreenRequest carries wayland → core fullscreen requests.
type WindowFullscreenRequest struct {
	ID         WindowID
	Fullscreen bool
}

func (WindowFullscreenRequest) clientEvent() {}

// WindowAppID carries wayland → core an app ID set after the window mapped.
type WindowAppID struct {
	ID    WindowID
	AppID string
}

func (WindowAppID) clientEvent() {}

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
type PointerMotion struct {
	X, Y     float64
	TimeMsec uint32
}

func (PointerMotion) inputEvent() {}

// PointerButton uses evdev button codes (BTN_LEFT is 0x110).
type PointerButton struct {
	Button   uint32
	Pressed  bool
	TimeMsec uint32
}

func (PointerButton) inputEvent() {}

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

// OutputRemoved carries output → core an unplugged display.
type OutputRemoved struct{ Name string }

func (OutputRemoved) outputEvent() {}

// ConfigChanged carries config → core reloads.
type ConfigChanged struct{ Config Config }

// ClientCommand carries core → wayland commands.
type ClientCommand interface{ clientCommand() }

// ConfigureWindow carries core → wayland geometry and state. Output is the
// connector showing the window, empty while it is hidden.
type ConfigureWindow struct {
	ID                    WindowID
	Width, Height         int
	Fullscreen, Activated bool
	Output                string
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

// PointerFocus changes the pointer surface; ID 0 clears focus. Coordinates are surface-local.
type PointerFocus struct {
	ID   WindowID
	X, Y float64
}

func (PointerFocus) clientCommand() {}

type PointerMotionTo struct {
	ID       WindowID
	X, Y     float64
	TimeMsec uint32
}

func (PointerMotionTo) clientCommand() {}

type PointerButtonTo struct {
	ID       WindowID
	Button   uint32
	Pressed  bool
	TimeMsec uint32
}

func (PointerButtonTo) clientCommand() {}

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
	Background                string
	Border                    Border
	Windows                   []SceneWindow
	// Layers are drawn in slice order: background and bottom before windows,
	// top and overlay after. A fullscreen window covers bottom and top.
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

// Border carries the window border style; colors are #rrggbb, "" skips drawing.
type Border struct {
	Width            int
	Active, Inactive string
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
	ID                 WindowID
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
	// Borderless windows fill the usable width alone; no border is drawn.
	Borderless bool
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
}
