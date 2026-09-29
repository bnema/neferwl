package libinput

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
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

func TestSwipesIgnoreSecondTouchpad(t *testing.T) {
	var s swipes
	const pad, other = 1, 2
	s.begin(pad, 3, time.Second)
	if ev := s.begin(other, 3, time.Second); ev != nil {
		t.Fatalf("second touchpad began: %v", ev)
	}
	if ev := s.update(other, 1, 1, time.Second); ev != nil {
		t.Fatalf("second touchpad moved: %v", ev)
	}
	if ev := s.end(other, false, time.Second); ev != nil {
		t.Fatalf("second touchpad ended the swipe: %v", ev)
	}
	if ev := s.update(pad, 1, 1, 2*time.Second); ev == nil {
		t.Fatal("first touchpad lost its swipe")
	}
	s.end(pad, false, 3*time.Second)
	if ev := s.begin(other, 3, 4*time.Second); ev == nil {
		t.Fatal("second touchpad cannot swipe after the first ended")
	}
}

func TestSwipesStreamThreeFingers(t *testing.T) {
	var s swipes
	const pad, other = 1, 2
	if ev := s.begin(pad, 4, time.Second); ev != nil {
		t.Fatalf("four fingers: %v", ev)
	}
	if ev := s.update(pad, 1, 1, time.Second); ev != nil {
		t.Fatalf("update without a swipe: %v", ev)
	}
	if ev := s.begin(pad, 3, time.Second); ev != (ports.SwipeBegin{Time: time.Second}) {
		t.Fatalf("begin: %v", ev)
	}
	if ev := s.update(pad, -3, 2, 2*time.Second); ev != (ports.SwipeUpdate{DX: -3, DY: 2, Time: 2 * time.Second}) {
		t.Fatalf("update: %v", ev)
	}
	if ev := s.end(other, false, 0); ev != nil {
		t.Fatalf("end on another device: %v", ev)
	}
	if ev := s.end(pad, true, 3*time.Second); ev != (ports.SwipeEnd{Cancelled: true, Time: 3 * time.Second}) {
		t.Fatalf("end: %v", ev)
	}
	if ev := s.end(pad, false, 0); ev != nil {
		t.Fatalf("second end: %v", ev)
	}
}

func TestAccelProfile(t *testing.T) {
	if accelProfile(ports.AccelFlat) != 1 || accelProfile(ports.AccelAdaptive) != 2 {
		t.Fatal(accelProfile(ports.AccelFlat), accelProfile(ports.AccelAdaptive))
	}
}

func TestTimestamps(t *testing.T) {
	if usec(1500) != 1500*time.Microsecond || msec(1_234_567) != 1234 {
		t.Fatal(usec(1500), msec(1_234_567))
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
	if m := p.move(-80, 100); m.X != 0 || m.Y != 49 {
		t.Fatalf("got %v %v", m.X, m.Y)
	}
	// Into B, where deltas count in physical pixels (scale 2).
	p.set(150, 20)
	if m := p.move(20, 10); m.X != 160 || m.Y != 25 || m.DX != 10 || m.DY != 5 {
		t.Fatalf("got %+v", m)
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

func TestPointerStartsOnPrimary(t *testing.T) {
	p := newPointer(ports.Layout{
		{Info: ports.OutputInfo{Name: "A"}, Width: 100, Height: 50, Scale: 1},
		{Info: ports.OutputInfo{Name: "B"}, X: 100, Width: 200, Height: 100, Scale: 1, Primary: true},
	})
	if p.x != 200 || p.y != 50 {
		t.Fatal(p.x, p.y)
	}
}

func TestPointerConstraints(t *testing.T) {
	p := newPointer(ports.Layout{{Info: ports.OutputInfo{Name: "A"}, Width: 100, Height: 100, Scale: 1}})
	p.constrain(ports.PointerConstraint{Mode: ports.ConstraintLock, X: 50, Y: 50})
	if m := p.move(10, -5); m.X != 50 || m.Y != 50 || m.DX != 10 || m.DY != -5 {
		t.Fatalf("locked: %+v", m)
	}
	// Confining moves the pointer inside, then keeps it there.
	p.constrain(ports.PointerConstraint{Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 60, Y: 10, W: 20, H: 20}, X: 50, Y: 50})
	if p.x != 60 || p.y != 29 {
		t.Fatal(p.x, p.y)
	}
	if m := p.move(100, 0); m.X != 79 || m.DX != 100 {
		t.Fatalf("confined: %+v", m)
	}
	p.constrain(ports.PointerConstraint{})
	if m := p.move(10, 0); m.X != 89 {
		t.Fatalf("free: %+v", m)
	}
	// A lock takes core's cursor position, and so does its release.
	p.constrain(ports.PointerConstraint{Mode: ports.ConstraintLock, X: 20, Y: 30})
	if p.x != 20 || p.y != 30 {
		t.Fatal(p.x, p.y)
	}
	p.move(40, 40)
	p.constrain(ports.PointerConstraint{X: 21, Y: 31})
	if p.x != 21 || p.y != 31 {
		t.Fatal(p.x, p.y)
	}
}
