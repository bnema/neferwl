package ports

import "time"

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
// after this transition; wayland forwards both to clients unchanged. Time is
// the device timestamp (CLOCK_MONOTONIC), like SwipeBegin.Time.
type KeyEvent struct {
	Keysym string
	// Base is the key's unshifted keysym in the active layout, so Cmd+Shift+1
	// still matches a cmd+shift+1 bind although it prints exclam.
	Base    string
	Mods    Mods
	Pressed bool
	Time    time.Duration
	Keycode uint32
	State   ModState
}

// ModState is the serialized xkb modifier state sent in wl_keyboard.modifiers.
type ModState struct{ Depressed, Latched, Locked, Group uint32 }

func (KeyEvent) inputEvent() {}

// PointerMotion uses global layout coordinates in logical pixels (see OutputPlacement).
// DX and DY are the accelerated logical deltas and UnaccelDX, UnaccelDY the
// raw device deltas; all four are 0 for absolute devices. Time is the device
// timestamp (CLOCK_MONOTONIC).
type PointerMotion struct {
	X, Y                 float64
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
	Time                 time.Duration
}

func (PointerMotion) inputEvent() {}

// PointerButton uses evdev button codes (BTN_LEFT is 0x110). Time is the
// device timestamp (CLOCK_MONOTONIC).
type PointerButton struct {
	Button  uint32
	Pressed bool
	Time    time.Duration
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

// PointerAxis is one scroll frame: Vertical, then Horizontal. Time is the
// device timestamp (CLOCK_MONOTONIC).
type PointerAxis struct {
	Source               AxisSource
	Vertical, Horizontal ScrollAxis
	Time                 time.Duration
}

func (PointerAxis) inputEvent() {}

// SwipeBegin starts a touchpad swipe of any finger count; core keeps three
// and four fingers and forwards the others to the window under the
// pointer. Time is the device timestamp (CLOCK_MONOTONIC), shared by the
// updates and the end.
type SwipeBegin struct {
	Fingers int
	Time    time.Duration
}

// SwipeUpdate moves the fingers by the unaccelerated deltas DX, DY
// (touchpad units, as libinput reports them, not natural-scroll inverted),
// which the compositor's own swipes follow. AccelDX, AccelDY are the same
// move with pointer acceleration, what a client gets.
type SwipeUpdate struct {
	DX, DY           float64
	AccelDX, AccelDY float64
	Time             time.Duration
}

// SwipeEnd lifts the fingers. Cancelled is set when libinput cancelled
// the swipe (another finger landed).
type SwipeEnd struct {
	Cancelled bool
	Time      time.Duration
}

func (SwipeBegin) inputEvent()  {}
func (SwipeUpdate) inputEvent() {}
func (SwipeEnd) inputEvent()    {}

// PinchBegin starts a touchpad pinch. Time is the device timestamp
// (CLOCK_MONOTONIC), shared by the updates and the end. A pinch always goes
// to the window under the pointer.
type PinchBegin struct {
	Fingers int
	Time    time.Duration
}

// PinchUpdate moves a pinch: the fingers' centre by the deltas DX, DY with
// pointer acceleration, as clients get them, Scale is the spread of the fingers
// relative to the begin (1.0 there) and Rotation the angle turned since the
// previous event, in degrees clockwise.
type PinchUpdate struct {
	DX, DY   float64
	Scale    float64
	Rotation float64
	Time     time.Duration
}

// PinchEnd lifts the fingers. Cancelled is set when libinput cancelled the
// pinch.
type PinchEnd struct {
	Cancelled bool
	Time      time.Duration
}

// HoldBegin starts a touchpad hold: fingers resting on the pad. Like a
// pinch it goes to the window under the pointer.
type HoldBegin struct {
	Fingers int
	Time    time.Duration
}

// HoldEnd lifts the fingers. Cancelled is set when the fingers moved or
// another finger landed, so the hold became something else.
type HoldEnd struct {
	Cancelled bool
	Time      time.Duration
}

func (PinchBegin) inputEvent()  {}
func (PinchUpdate) inputEvent() {}
func (PinchEnd) inputEvent()    {}
func (HoldBegin) inputEvent()   {}
func (HoldEnd) inputEvent()     {}
