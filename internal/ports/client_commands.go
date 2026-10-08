package ports

import "time"

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
	// Output is the output the window belongs to, kept while it is not
	// visible (scale, foreign toplevel).
	Output string
	// Visible is set while part of the window is on its output: not on a
	// hidden workspace, scrolled off, behind a maximized column or a
	// hidden float. An invisible window is suspended and throttled.
	Visible bool
	// Captured marks a window drawn only for a capture session (its
	// workspace is not on screen): Visible stays the physical truth.
	Captured bool
}

func (ConfigureWindow) clientCommand() {}

// OutputPlacement is one output in the global layout. X, Y, Width and
// Height are logical (after Transform: they swap for 90/270);
// physical = logical × Scale. Info.Width and Info.Height are the physical
// mode, the untransformed target.
type OutputPlacement struct {
	Info                OutputInfo
	X, Y, Width, Height int
	Scale               float64
	// Transform is how the target holds the scene (wl_output.transform).
	Transform BufferTransform
	// Primary is output.<name>.primary: the pointer starts on it.
	Primary bool
}

// Contains reports whether the logical point is on the output.
func (o OutputPlacement) Contains(x, y float64) bool {
	return x >= float64(o.X) && x < float64(o.X+o.Width) && y >= float64(o.Y) && y < float64(o.Y+o.Height)
}

// ToTarget maps a global logical point on the output to target pixels.
func (o OutputPlacement) ToTarget(x, y float64) (float64, float64) {
	s := o.Scale
	if s <= 0 {
		s = 1
	}
	px, py := (x-float64(o.X))*s, (y-float64(o.Y))*s
	sw, sh := o.Transform.Size(o.Info.Width, o.Info.Height)
	return o.Transform.ToBuffer(px, py, float64(sw), float64(sh))
}

// Layout is the global arrangement of outputs in logical pixels.
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
// window coordinates. The deltas and Time feed zwp_relative_pointer_v1;
// While the window locks the pointer, wayland sends only relative motion.
type PointerMotionTo struct {
	ID                   WindowID
	X, Y                 float64
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
	// Time is the device timestamp (CLOCK_MONOTONIC).
	Time time.Duration
}

func (PointerMotionTo) clientCommand() {}

type PointerButtonTo struct {
	ID      WindowID
	Button  uint32
	Pressed bool
	// Time is the device timestamp (CLOCK_MONOTONIC).
	Time time.Duration
}

func (PointerButtonTo) clientCommand() {}

// PointerAxisTo sends a scroll frame to a window.
type PointerAxisTo struct {
	ID   WindowID
	Axis PointerAxis
}

func (PointerAxisTo) clientCommand() {}

// GestureKind is a touchpad gesture a client can receive
// (zwp_pointer_gestures_v1).
type GestureKind uint8

const (
	GestureSwipe GestureKind = iota
	GesturePinch
	GestureHold
)

// GestureBeginTo starts a gesture on a window with the pointer focus.
// Wayland assigns the serial. Time is the device timestamp
// (CLOCK_MONOTONIC), like the updates and the end of the gesture.
type GestureBeginTo struct {
	ID      WindowID
	Kind    GestureKind
	Fingers int
	Time    time.Duration
}

func (GestureBeginTo) clientCommand() {}

// GestureUpdateTo moves the gesture of a window. DX and DY are the
// centre's movement; Scale and Rotation are only for a pinch (see
// PinchUpdate). A hold has no updates.
type GestureUpdateTo struct {
	ID       WindowID
	Kind     GestureKind
	DX, DY   float64
	Scale    float64
	Rotation float64
	Time     time.Duration
}

func (GestureUpdateTo) clientCommand() {}

// GestureEndTo ends the gesture of a window. Cancelled is set when the
// gesture did not complete: the pointer focus moved, the window went away
// or the device cancelled it. Ending a gesture that is not running does
// nothing.
type GestureEndTo struct {
	ID        WindowID
	Kind      GestureKind
	Cancelled bool
	Time      time.Duration
}

func (GestureEndTo) clientCommand() {}

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
	// Security retains the admitting owner epoch through launcher backpressure.
	// A wired launcher rejects stale or protected requests before execution.
	Security SecurityState
	Argv     []string
	// Env adds KEY=value entries to the child environment.
	Env []string
}
