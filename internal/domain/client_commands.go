package domain

// GestureKind is a touchpad gesture a client can receive
// (zwp_pointer_gestures_v1).
type GestureKind uint8

const (
	GestureSwipe GestureKind = iota
	GesturePinch
	GestureHold
)
