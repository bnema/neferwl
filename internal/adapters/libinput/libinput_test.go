package libinput

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

func TestHotkey(t *testing.T) {
	ca := ports.ModCtrl | ports.ModAlt
	for _, tc := range []struct {
		name   string
		ev     ports.KeyEvent
		action hotkeyAction
		vt     int
	}{
		{"ctrl alt backspace", ports.KeyEvent{Keysym: "BackSpace", Keycode: 14, Mods: ca, Pressed: true}, hotkeyQuit, 0},
		{"release ignored", ports.KeyEvent{Keysym: "BackSpace", Keycode: 14, Mods: ca}, hotkeyNone, 0},
		{"ctrl only", ports.KeyEvent{Keysym: "BackSpace", Keycode: 14, Mods: ports.ModCtrl, Pressed: true}, hotkeyNone, 0},
		{"xkb vt sym", ports.KeyEvent{Keysym: "XF86Switch_VT_3", Keycode: 61, Mods: ca, Pressed: true}, hotkeyVT, 3},
		{"ctrl alt f2 raw", ports.KeyEvent{Keysym: "F2", Keycode: 60, Mods: ca, Pressed: true}, hotkeyVT, 2},
		{"ctrl alt f12 raw", ports.KeyEvent{Keysym: "F12", Keycode: 88, Mods: ca, Pressed: true}, hotkeyVT, 12},
		{"plain f2", ports.KeyEvent{Keysym: "F2", Keycode: 60, Pressed: true}, hotkeyNone, 0},
	} {
		a, vt := hotkey(tc.ev)
		if a != tc.action || vt != tc.vt {
			t.Errorf("%s: got %v %d", tc.name, a, vt)
		}
	}
}

func TestPointerClamp(t *testing.T) {
	p := pointer{x: 10, y: 10, w: 100, h: 50}
	if x, y := p.move(-20, 100); x != 0 || y != 49 {
		t.Fatalf("got %v %v", x, y)
	}
	if x, y := p.move(30.5, -5); x != 30.5 || y != 44 {
		t.Fatalf("got %v %v", x, y)
	}
}
