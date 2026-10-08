package libinput

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/xkb"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
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
		for _, protected := range []bool{false, true} {
			want := tc.action
			if protected && want == hotkeyQuit {
				want = hotkeyNone
			}
			a, vt := hotkey(tc.ev, protected)
			if a != want || vt != tc.vt {
				t.Errorf("%s protected=%v: got %v %d", tc.name, protected, a, vt)
			}
		}
	}
}

func TestGesturesIgnoreSecondTouchpad(t *testing.T) {
	var g gestures
	const pad, other = 1, 2
	g.begin(pad, ports.GestureSwipe, 3, time.Second)
	if ev := g.begin(other, ports.GestureSwipe, 3, time.Second); ev != nil {
		t.Fatalf("second touchpad began: %v", ev)
	}
	if ev := g.swipeUpdate(other, 1, 1, time.Second); ev != nil {
		t.Fatalf("second touchpad moved: %v", ev)
	}
	if ev := g.end(other, ports.GestureSwipe, false, time.Second); ev != nil {
		t.Fatalf("second touchpad ended the swipe: %v", ev)
	}
	if ev := g.swipeUpdate(pad, 1, 1, 2*time.Second); ev == nil {
		t.Fatal("first touchpad lost its swipe")
	}
	g.end(pad, ports.GestureSwipe, false, 3*time.Second)
	if ev := g.begin(other, ports.GestureSwipe, 3, 4*time.Second); ev == nil {
		t.Fatal("second touchpad cannot swipe after the first ended")
	}
}

func TestGesturesStreamSwipe(t *testing.T) {
	var g gestures
	const pad, other = 1, 2
	if ev := g.begin(pad, ports.GestureSwipe, 0, time.Second); ev != nil {
		t.Fatalf("no fingers: %v", ev)
	}
	if ev := g.swipeUpdate(pad, 1, 1, time.Second); ev != nil {
		t.Fatalf("update without a swipe: %v", ev)
	}
	if ev := g.begin(pad, ports.GestureSwipe, 3, time.Second); ev != (ports.SwipeBegin{Fingers: 3, Time: time.Second}) {
		t.Fatalf("begin: %v", ev)
	}
	if ev := g.swipeUpdate(pad, -3, 2, 2*time.Second); ev != (ports.SwipeUpdate{DX: -3, DY: 2, Time: 2 * time.Second}) {
		t.Fatalf("update: %v", ev)
	}
	if ev := g.end(other, ports.GestureSwipe, false, 0); ev != nil {
		t.Fatalf("end on another device: %v", ev)
	}
	if ev := g.end(pad, ports.GestureSwipe, true, 3*time.Second); ev != (ports.SwipeEnd{Cancelled: true, Time: 3 * time.Second}) {
		t.Fatalf("end: %v", ev)
	}
	if ev := g.end(pad, ports.GestureSwipe, false, 0); ev != nil {
		t.Fatalf("second end: %v", ev)
	}
}

// Swipes of every finger count stream; core picks which ones it keeps.
func TestGesturesStreamAnyFingerCount(t *testing.T) {
	for _, fingers := range []int{2, 3, 4, 5} {
		var g gestures
		if ev := g.begin(1, ports.GestureSwipe, fingers, time.Second); ev != (ports.SwipeBegin{Fingers: fingers, Time: time.Second}) {
			t.Fatalf("%d fingers: %v", fingers, ev)
		}
		if ev := g.begin(1, ports.GestureSwipe, 5, time.Second); ev != nil {
			t.Fatalf("a second swipe began during a swipe: %v", ev)
		}
	}
}

func TestGesturesStreamPinch(t *testing.T) {
	var g gestures
	const pad = 1
	if ev := g.begin(pad, ports.GesturePinch, 2, time.Second); ev != (ports.PinchBegin{Fingers: 2, Time: time.Second}) {
		t.Fatalf("begin: %v", ev)
	}
	if ev := g.swipeUpdate(pad, 1, 1, time.Second); ev != nil {
		t.Fatalf("swipe update during a pinch: %v", ev)
	}
	if ev := g.end(pad, ports.GestureHold, false, time.Second); ev != nil {
		t.Fatalf("hold end during a pinch: %v", ev)
	}
	want := ports.PinchUpdate{DX: 1, DY: -2, Scale: 1.5, Rotation: -10, Time: 2 * time.Second}
	if ev := g.pinchUpdate(pad, 1, -2, 1.5, -10, 2*time.Second); ev != want {
		t.Fatalf("update: %v", ev)
	}
	if ev := g.end(pad, ports.GesturePinch, false, 3*time.Second); ev != (ports.PinchEnd{Time: 3 * time.Second}) {
		t.Fatalf("end: %v", ev)
	}
	if ev := g.pinchUpdate(pad, 1, 1, 1, 0, 4*time.Second); ev != nil {
		t.Fatalf("update after the end: %v", ev)
	}
}

func TestGesturesStreamHold(t *testing.T) {
	var g gestures
	const pad = 1
	if ev := g.begin(pad, ports.GestureHold, 3, time.Second); ev != (ports.HoldBegin{Fingers: 3, Time: time.Second}) {
		t.Fatalf("begin: %v", ev)
	}
	if ev := g.begin(pad, ports.GesturePinch, 2, time.Second); ev != nil {
		t.Fatalf("pinch began during a hold: %v", ev)
	}
	if ev := g.end(pad, ports.GestureHold, true, 2*time.Second); ev != (ports.HoldEnd{Cancelled: true, Time: 2 * time.Second}) {
		t.Fatalf("end: %v", ev)
	}
}

// A device that goes during a gesture ends it cancelled, whatever its kind.
func TestGesturesAbortOnRemoval(t *testing.T) {
	for kind, want := range map[ports.GestureKind]ports.InputEvent{
		ports.GestureSwipe: ports.SwipeEnd{Cancelled: true},
		ports.GesturePinch: ports.PinchEnd{Cancelled: true},
		ports.GestureHold:  ports.HoldEnd{Cancelled: true},
	} {
		var g gestures
		g.begin(1, kind, 3, time.Second)
		if ev := g.abort(2); ev != nil {
			t.Fatalf("kind %d: another device aborted: %v", kind, ev)
		}
		if ev := g.abort(1); ev != want {
			t.Fatalf("kind %d: abort %v, want %v", kind, ev, want)
		}
		if ev := g.abort(1); ev != nil {
			t.Fatalf("kind %d: second abort %v", kind, ev)
		}
		if ev := g.begin(1, kind, 3, time.Second); ev == nil {
			t.Fatalf("kind %d: no gesture after the abort", kind)
		}
	}
}

func TestAccelProfile(t *testing.T) {
	if accelProfile(ports.AccelFlat) != 1 || accelProfile(ports.AccelAdaptive) != 2 {
		t.Fatal(accelProfile(ports.AccelFlat), accelProfile(ports.AccelAdaptive))
	}
}

func TestTimestamps(t *testing.T) {
	if usec(1500) != 1500*time.Microsecond {
		t.Fatal(usec(1500))
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
	p.moved(func(o string, x, y float64, _ bool) { out, px, py = o, x, y }, true)
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
	// A warp moves a free pointer to core's cursor.
	p.constrain(ports.PointerConstraint{X: 70, Y: 40, Warp: true})
	if p.x != 70 || p.y != 40 {
		t.Fatal(p.x, p.y)
	}
}

func TestProtectedKeyboardPolicy(t *testing.T) {
	ca := ports.ModCtrl | ports.ModAlt
	for _, key := range []ports.KeyEvent{
		{Keysym: "credential-secret", Base: "raw-secret", Keycode: 424242, Pressed: true},
		{Keysym: "credential-secret", Keycode: 424242},
		{Keysym: "BackSpace", Mods: ca, Pressed: true},
		{Keysym: "raw-secret", Keycode: 14, Mods: ca, Pressed: true},
	} {
		t.Run(key.Keysym, func(t *testing.T) {
			var buf bytes.Buffer
			security := portsmocks.NewMockSessionSecurity(t)
			state := ports.SecurityState{Generation: 7, Protected: true}
			security.EXPECT().Snapshot().Return(state).Once()
			opts := Options{Security: security, LogKeys: true, Log: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &buf})}
			produced := securitySnapshot(opts.Security)
			ev, err := translateKey(key, opts, produced)
			if err != nil || errors.Is(err, ErrEmergencyQuit) || ev != key {
				t.Fatalf("protected key lost/quit: event=%#v err=%v", ev, err)
			}
			if got := secureInput(ev, opts.Security, produced); got != (ports.SecurityInput{State: state, Event: key}) {
				t.Fatalf("wrong production epoch: %#v", got)
			}
			if buf.Len() != 0 {
				t.Fatalf("protected key logged identifiers: %s", &buf)
			}
		})
	}
}

func TestKeyboardPolicyUnlockedAndVT(t *testing.T) {
	ca := ports.ModCtrl | ports.ModAlt
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlocked", true: "protected"}[protected], func(t *testing.T) {
			var buf bytes.Buffer
			security := portsmocks.NewMockSessionSecurity(t)
			state := ports.SecurityState{Generation: 9, Protected: protected}
			security.EXPECT().Snapshot().Return(state).Once()
			seat := portsmocks.NewMockSeat(t)
			seat.EXPECT().SwitchVT(2).Once()
			opts := Options{Security: security, Seat: seat, LogKeys: true, Log: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &buf})}
			ev, err := translateKey(ports.KeyEvent{Keysym: "XF86Switch_VT_2", Keycode: 60, Mods: ca, Pressed: true}, opts, securitySnapshot(opts.Security))
			if ev != nil || err != nil {
				t.Fatalf("VT not consumed: %#v %v", ev, err)
			}
			if protected && buf.Len() != 0 {
				t.Fatalf("protected VT key logged: %s", &buf)
			}
			if !protected && !strings.Contains(buf.String(), "XF86Switch_VT_2") {
				t.Fatalf("unlocked debug logging changed: %s", &buf)
			}
		})
	}
	var buf bytes.Buffer
	opts := Options{LogKeys: true, Log: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &buf})}
	key := ports.KeyEvent{Keysym: "q", Keycode: 16, Pressed: true}
	state := securitySnapshot(nil)
	ev, err := translateKey(key, opts, state)
	if err != nil || secureInput(ev, nil, state) != key || !strings.Contains(buf.String(), `"keysym":"q"`) {
		t.Fatalf("nil gate changed unlocked behavior: %#v %v %s", ev, err, &buf)
	}
	ev, err = translateKey(ports.KeyEvent{Keysym: "BackSpace", Mods: ca, Pressed: true}, opts, state)
	if ev != nil || !errors.Is(err, ErrEmergencyQuit) || !strings.Contains(buf.String(), "emergency-key") {
		t.Fatalf("unlocked emergency quit changed: %#v %v %s", ev, err, &buf)
	}
}

func TestSecurityInputAllKindsProductionEpoch(t *testing.T) {
	security := portsmocks.NewMockSessionSecurity(t)
	old := ports.SecurityState{Generation: 11, Protected: true}
	newState := ports.SecurityState{Generation: 12}
	events := []ports.InputEvent{
		ports.KeyEvent{Keysym: "secret", Pressed: true}, ports.PointerMotion{X: 1},
		ports.PointerButton{Button: 272}, ports.PointerAxis{},
		ports.SwipeBegin{}, ports.SwipeUpdate{}, ports.SwipeEnd{},
		ports.PinchBegin{}, ports.PinchUpdate{}, ports.PinchEnd{}, ports.HoldBegin{}, ports.HoldEnd{},
	}
	security.EXPECT().Snapshot().Return(old).Times(len(events))
	security.EXPECT().Snapshot().Return(newState).Once()
	f := newForwarder(zerowrap.Default())
	for _, ev := range events {
		f.push(secureInput(ev, security, securitySnapshot(security)))
	}
	if securitySnapshot(security) != newState {
		t.Fatal("did not transition")
	}
	for _, want := range events {
		got, ok := f.pop()
		if !ok || got != (ports.SecurityInput{State: old, Event: want}) {
			t.Fatalf("queued event changed epoch: %#v", got)
		}
	}
	if secureInput(nil, security, old) != nil {
		t.Fatal("nil translated event was wrapped")
	}
}

func TestKeyboardProducerQuarantinesBeforeHotkeysAndLogging(t *testing.T) {
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		t.Fatal(err)
	}
	defer km.Close()
	initial := ports.SecurityState{}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	unlocked := ports.SecurityState{Generation: 2}
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().Return(initial).Times(3)
	security.EXPECT().Snapshot().Return(locked).Times(4)
	security.EXPECT().Snapshot().Return(unlocked).Times(5)
	var buf bytes.Buffer
	opts := Options{Keymap: km, Security: security, LogKeys: true, Log: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &buf})}
	produce := func(code uint32, down bool) ports.InputEvent {
		t.Helper()
		state := securitySnapshot(opts.Security)
		ev, err := translateKeyboard(code, down, 123*time.Millisecond, opts, state)
		if err != nil {
			t.Fatal(err)
		}
		return secureInput(ev, opts.Security, state)
	}
	for _, code := range []uint32{125, 29, 56} {
		produce(code, true)
	}
	buf.Reset()
	for _, code := range []uint32{125, 29, 56} {
		if ev := produce(code, true); ev != nil {
			t.Fatalf("inherited repeat delivered: %#v", ev)
		}
	}
	// Ctrl+Alt held before lock must not activate emergency quit, even on a
	// fresh Backspace. Super must not contaminate the credential either.
	if ev := produce(14, true).(ports.SecurityInput).Event.(ports.KeyEvent); ev.Mods != 0 || ev.State != (ports.ModState{}) {
		t.Fatalf("protected fresh key inherited native state: %+v", ev)
	}
	if buf.Len() != 0 {
		t.Fatalf("protected keyboard logged: %s", &buf)
	}
	if ev := produce(16, true).(ports.SecurityInput); ev.State != unlocked || ev.Event.(ports.KeyEvent).Mods != 0 || ev.Event.(ports.KeyEvent).State != (ports.ModState{}) {
		t.Fatalf("unlocked q inherited state: %#v", ev)
	}
	buf.Reset()
	for _, code := range []uint32{14, 56, 29, 125} {
		if ev := produce(code, false); ev != nil {
			t.Fatalf("inherited release delivered: %#v", ev)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("quarantined release logged: %s", &buf)
	}
}

// The cursor is placed in target pixels: after the output transform.
func TestPointerMovedTransform(t *testing.T) {
	p := newPointer(ports.Layout{{Info: ports.OutputInfo{Name: "A", Width: 200, Height: 100}, Scale: 1, Transform: 1, Width: 100, Height: 200}})
	p.set(10, 20)
	var out string
	var px, py float64
	p.moved(func(o string, x, y float64, _ bool) { out, px, py = o, x, y }, true)
	if out != "A" || px != 20 || py != 90 {
		t.Fatal(out, px, py)
	}
}
