package ports

// ClientEvent carries wayland → core notifications.
type ClientEvent interface{ clientEvent() }

// WindowMapped carries wayland → core mapping.
type WindowMapped struct {
	ID    WindowID
	AppID string
	// Slot is the SlotEnv value of the client process, empty if none.
	Slot string
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

// PointerMotion uses absolute output coordinates in physical pixels.
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

// OutputMode carries output → core resolution changes in physical pixels.
// Name is the connector, matched against output.<name>.scale.
type OutputMode struct {
	Width, Height int
	Name          string
}

func (OutputMode) outputEvent() {}

// OutputUsable carries output → core usable area after layer-shell exclusive zones.
type OutputUsable struct{ Rect Rect }

func (OutputUsable) outputEvent() {}

// ConfigChanged carries config → core reloads.
type ConfigChanged struct{ Config Config }

// ClientCommand carries core → wayland commands.
type ClientCommand interface{ clientCommand() }

// ConfigureWindow carries core → wayland geometry and state.
type ConfigureWindow struct {
	ID                    WindowID
	Width, Height         int
	Fullscreen, Activated bool
}

func (ConfigureWindow) clientCommand() {}

// SetOutputScale carries core → wayland the output scale and its logical size.
// Window and pointer coordinates in other commands are logical.
type SetOutputScale struct {
	Scale         float64
	Width, Height int
}

func (SetOutputScale) clientCommand() {}

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

// Scene carries core → renderer immutable snapshots with fresh Windows slices.
// Rects and the output size are logical; the renderer multiplies by Scale.
type Scene struct {
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

// SurfaceContent carries wayland → output the latest committed pixels of a
// window. Pixels is a private copy in B8G8R8A8 (wl_shm argb8888/xrgb8888
// little-endian) with Stride bytes per row; receivers never modify it.
// Pixels == nil means the window has no content. LogicalW and LogicalH are
// the surface size in logical pixels (buffer scale and viewport applied).
type SurfaceContent struct {
	ID                 WindowID
	Width, Height      int
	LogicalW, LogicalH int
	Stride             int
	Opaque             bool // xrgb8888: ignore the alpha byte
	Pixels             []byte
}

// SceneWindow carries core → renderer window placement.
type SceneWindow struct {
	ID                          WindowID
	Rect                        Rect
	Focused, Fullscreen, Hidden bool
	// Borderless windows fill the usable width alone; no border is drawn.
	Borderless bool
}
