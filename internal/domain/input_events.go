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
