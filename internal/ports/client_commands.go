package ports

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// ClientCommand carries core → wayland commands.
type ClientCommand interface{ clientCommand() }

// ShortcutsInhibitState carries core → wayland whether a window's
// shortcuts inhibitor is in effect (it has keyboard focus).
type ShortcutsInhibitState struct {
	Window domain.WindowID
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
	ID                    domain.WindowID
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

// SetOutputs carries core → wayland every output and the focused one (where
// new surfaces start). It is sent when the list, a placement or the focus changes.
type SetOutputs struct {
	Outputs domain.Layout
	Focused string
	// Off lists the outputs a client turned off (output power management),
	// in Outputs order.
	Off []string
}

func (SetOutputs) clientCommand() {}

// CloseWindow carries core → wayland close requests.
type CloseWindow struct{ ID domain.WindowID }

func (CloseWindow) clientCommand() {}

// FocusWindow carries core → wayland keyboard focus (0 means none).
type FocusWindow struct{ ID domain.WindowID }

func (FocusWindow) clientCommand() {}

// ConfigurePopup carries core → wayland a popup's place, relative to its
// parent's window geometry. Reposition answers a reposition request.
type ConfigurePopup struct {
	ID         domain.WindowID
	Rect       domain.Rect
	Reposition bool
	Token      uint32
}

func (ConfigurePopup) clientCommand() {}

// ClosePopup carries core → wayland a popup to dismiss (popup_done).
type ClosePopup struct{ ID domain.WindowID }

func (ClosePopup) clientCommand() {}

// PointerFocus changes the pointer surface; ID 0 clears focus. Coordinates are surface-local.
type PointerFocus struct {
	ID   domain.WindowID
	X, Y float64
}

func (PointerFocus) clientCommand() {}

// PointerMotionTo carries core → wayland motion for the pointer focus, in
// window coordinates. The deltas and Time feed zwp_relative_pointer_v1;
// While the window locks the pointer, wayland sends only relative motion.
type PointerMotionTo struct {
	ID                   domain.WindowID
	X, Y                 float64
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
	// Time is the device timestamp (CLOCK_MONOTONIC).
	Time time.Duration
}

func (PointerMotionTo) clientCommand() {}

type PointerButtonTo struct {
	ID      domain.WindowID
	Button  uint32
	Pressed bool
	// Grab marks the press that starts the implicit grab: no other button
	// was held. start_drag must name its serial.
	Grab bool
	// Time is the device timestamp (CLOCK_MONOTONIC).
	Time time.Duration
}

func (PointerButtonTo) clientCommand() {}

// PointerAxisTo sends a scroll frame to a window.
type PointerAxisTo struct {
	ID   domain.WindowID
	Axis PointerAxis
}

func (PointerAxisTo) clientCommand() {}

// GestureBeginTo starts a gesture on a window with the pointer focus.
// Wayland assigns the serial. Time is the device timestamp
// (CLOCK_MONOTONIC), like the updates and the end of the gesture.
type GestureBeginTo struct {
	ID      domain.WindowID
	Kind    domain.GestureKind
	Fingers int
	Time    time.Duration
}

func (GestureBeginTo) clientCommand() {}

// GestureUpdateTo moves the gesture of a window. DX and DY are the
// centre's movement; Scale and Rotation are only for a pinch (see
// PinchUpdate). A hold has no updates.
type GestureUpdateTo struct {
	ID       domain.WindowID
	Kind     domain.GestureKind
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
	ID        domain.WindowID
	Kind      domain.GestureKind
	Cancelled bool
	Time      time.Duration
}

func (GestureEndTo) clientCommand() {}

// ForwardKey carries core → wayland unbound keys.
type ForwardKey struct {
	ID  domain.WindowID
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

// SetModifiers carries core → wayland a temporary modifier override for the
// focused window's keyboards: Mods are sent as depressed modifiers (mapped
// to the current keymap's masks), latched/locked/group kept from the real
// state. Mods 0 with Restore true sends the real modifier state again: State
// is that state as core last saw it, which the seat adopts.
type SetModifiers struct {
	ID      domain.WindowID
	Mods    domain.Mods
	Restore bool
	State   domain.ModState
}

func (SetModifiers) clientCommand() {}

// CursorOverride carries core -> wayland the cursor a compositor pointer
// interaction (border resize, future column move) shows instead of the
// client's: a CSS cursor name; "" clears it.
type CursorOverride struct{ Shape string }

func (CursorOverride) clientCommand() {}

// WatchStats carries core → wayland the window whose frame statistics
// wayland reports (WindowStats) every StatsInterval; 0 stops reporting.
type WatchStats struct{ Window domain.WindowID }

func (WatchStats) clientCommand() {}

// StatsInterval is how often wayland reports the watched window's stats.
const StatsInterval = 500 * time.Millisecond
