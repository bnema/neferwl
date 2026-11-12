package libinput

import (
	"testing"

	"bnema.dev/purego"
)

func TestFixedArityIntegerCalls(t *testing.T) {
	lib, err := purego.Dlopen("libinput.so.10", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Skipf("libinput.so.10 unavailable: %v", err)
	}
	defer func() {
		if err := purego.Dlclose(lib); err != nil {
			t.Error(err)
		}
	}()
	if err := load(); err != nil {
		t.Fatal(err)
	}
	for name, addr := range map[string]uintptr{
		"dispatch":        sym.dispatch,
		"getEvent":        sym.getEvent,
		"eventType":       sym.eventType,
		"eventDestroy":    sym.eventDestroy,
		"eventDevice":     sym.eventDevice,
		"keyboardEvent":   sym.keyboardEvent,
		"keyboardKey":     sym.keyboardKey,
		"keyboardState":   sym.keyboardState,
		"keyboardUsec":    sym.keyboardUsec,
		"pointerEvent":    sym.pointerEvent,
		"pointerUsec":     sym.pointerUsec,
		"pointerButton":   sym.pointerButton,
		"pointerBtnState": sym.pointerBtnState,
		"pointerHasAxis":  sym.pointerHasAxis,
		"gestureEvent":    sym.gestureEvent,
		"gestureFingers":  sym.gestureFingers,
		"gestureCanceled": sym.gestureCanceled,
		"gestureUsec":     sym.gestureUsec,
	} {
		if addr == 0 {
			t.Errorf("%s symbol is zero", name)
		}
	}
}
