package ports

// ClientEvent carries wayland → core notifications.
type ClientEvent interface{ clientEvent() }

// WindowMapped carries wayland → core mapping.
type WindowMapped struct {
	ID    WindowID
	AppID string
}

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
type KeyEvent struct {
	Keysym   string
	Mods     Mods
	Pressed  bool
	TimeMsec uint32
}

func (KeyEvent) inputEvent() {}

// OutputEvent carries output → core notifications.
type OutputEvent interface{ outputEvent() }

// OutputMode carries output → core resolution changes.
type OutputMode struct{ Width, Height int }

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

// CloseWindow carries core → wayland close requests.
type CloseWindow struct{ ID WindowID }

func (CloseWindow) clientCommand() {}

// FocusWindow carries core → wayland keyboard focus (0 means none).
type FocusWindow struct{ ID WindowID }

func (FocusWindow) clientCommand() {}

// ForwardKey carries core → wayland unbound keys.
type ForwardKey struct {
	ID  WindowID
	Key KeyEvent
}

func (ForwardKey) clientCommand() {}

// SpawnRequest carries core → launcher process arguments.
type SpawnRequest struct{ Argv []string }

// Scene carries core → renderer immutable snapshots with fresh Windows slices.
type Scene struct {
	Seq                       uint64
	OutputWidth, OutputHeight int
	Background                string
	Windows                   []SceneWindow
}

// SceneWindow carries core → renderer window placement.
type SceneWindow struct {
	ID                          WindowID
	Rect                        Rect
	Focused, Fullscreen, Hidden bool
}
