package domain

// Mods carries input → core modifier flags.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
)

// AxisSource tells how a scroll was made; values match wl_pointer.axis_source.
type AxisSource uint8

const (
	AxisWheel AxisSource = iota
	AxisFinger
	AxisContinuous
)

// ModState is the serialized xkb modifier state sent in wl_keyboard.modifiers.
type ModState struct{ Depressed, Latched, Locked, Group uint32 }

// ScrollAxis is one scroll direction in a pointer frame. Value is in
// surface pixels; V120 counts wheel detents in 1/120 steps (wheel only).
// Stop ends a finger or continuous scroll on this axis.
type ScrollAxis struct {
	Set   bool
	Value float64
	V120  int32
	Stop  bool
}
