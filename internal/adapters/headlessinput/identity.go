package headlessinput

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func withDevice(ev ports.InputEvent, id domain.InputDeviceID) ports.InputEvent {
	switch e := ev.(type) {
	case ports.KeyEvent:
		e.Device = id
		return e
	case ports.PointerMotion:
		e.Device = id
		return e
	case ports.PointerButton:
		e.Device = id
		return e
	case ports.PointerAxis:
		e.Device = id
		return e
	case ports.SwipeBegin:
		e.Device = id
		return e
	case ports.SwipeUpdate:
		e.Device = id
		return e
	case ports.SwipeEnd:
		e.Device = id
		return e
	case ports.PinchBegin:
		e.Device = id
		return e
	case ports.PinchUpdate:
		e.Device = id
		return e
	case ports.PinchEnd:
		e.Device = id
		return e
	case ports.HoldBegin:
		e.Device = id
		return e
	case ports.HoldEnd:
		e.Device = id
		return e
	}
	return ev
}
