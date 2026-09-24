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

func TestPointerAcrossOutputs(t *testing.T) {
	layout := ports.Layout{
		{Info: ports.OutputInfo{Name: "A"}, Width: 100, Height: 50, Scale: 1},
		{Info: ports.OutputInfo{Name: "B"}, X: 100, Width: 200, Height: 100, Scale: 2},
	}
	p := newPointer(layout)
	if p.x != 50 || p.y != 25 {
		t.Fatal(p.x, p.y)
	}
	// Off the bottom of A: clamped to A.
	if x, y := p.move(-80, 100); x != 0 || y != 49 {
		t.Fatalf("got %v %v", x, y)
	}
	// Into B, where deltas count in physical pixels (scale 2).
	p.set(150, 20)
	if x, y := p.move(20, 10); x != 160 || y != 25 {
		t.Fatalf("got %v %v", x, y)
	}
	var out string
	var px, py float64
	p.moved(func(o string, x, y float64) { out, px, py = o, x, y })
	if out != "B" || px != 120 || py != 50 {
		t.Fatal(out, px, py)
	}
	// B unplugged: the pointer lands on A.
	p.setLayout(layout[:1])
	if p.x != 99 || p.y != 25 {
		t.Fatal(p.x, p.y)
	}
}
