package control

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func injectKeymap(t *testing.T) *xkb.Keymap {
	t.Helper()
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		if strings.Contains(err.Error(), "libxkbcommon.so.0") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	t.Cleanup(km.Close)
	return km
}

func TestInjectKeys(t *testing.T) {
	for _, tt := range []struct {
		method, text string
		names        []string
		codes        []uint32
	}{
		{"type", "Hi!", []string{"Shift_L", "H", "H", "Shift_L", "i", "i", "Shift_L", "exclam", "exclam", "Shift_L"}, nil},
		{"key", "Shift+A", []string{"Shift_L", "A", "A", "Shift_L"}, []uint32{42, 30, 30, 42}},
		{"key", "Super+Return", []string{"Super_L", "Return", "Return", "Super_L"}, []uint32{125, 28, 28, 125}},
		{"press", "Super", []string{"Super_L"}, []uint32{125}},
		{"release", "Super", []string{"Super_L"}, []uint32{125}},
		{"press", "Shift", []string{"Shift_L"}, []uint32{42}},
		{"press", "Ctrl", []string{"Control_L"}, []uint32{29}},
		{"press", "Alt", []string{"Alt_L"}, []uint32{56}},
	} {
		t.Run(tt.method+tt.text, func(t *testing.T) {
			i := newInjector(injectKeymap(t), nil)
			methods := map[string]func(string) ([]ports.InputEvent, error){"type": i.Type, "key": i.Key, "press": i.Press, "release": i.Release}
			events, err := methods[tt.method](tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != len(tt.names) {
				t.Fatalf("events %v", events)
			}
			for n, raw := range events {
				ev := raw.(ports.KeyEvent)
				if ev.Device != controlDeviceID || ev.Keysym != tt.names[n] || tt.codes != nil && ev.Keycode != tt.codes[n] {
					t.Fatalf("event %d: %+v", n, ev)
				}
				if tt.text == "Super+Return" && (n == 1 && ev.Mods&domain.ModSuper == 0 || n == 3 && ev.Mods&domain.ModSuper != 0) {
					t.Fatal(ev)
				}
			}
		})
	}
}

func TestInjectModifierAcrossPointer(t *testing.T) {
	i := newInjector(injectKeymap(t), nil)
	events, err := i.Press("Super")
	if err != nil || !events[0].(ports.KeyEvent).Pressed {
		t.Fatal(events, err)
	}
	for _, cmd := range []struct {
		name string
		args []string
	}{{"move", []string{"500", "100"}}, {"down", []string{"right"}}, {"move", []string{"700", "100"}}, {"up", []string{"right"}}} {
		if _, err := i.Pointer(cmd.name, cmd.args); err != nil {
			t.Fatal(err)
		}
	}
	events, err = i.Press("h")
	if err != nil || events[0].(ports.KeyEvent).Mods&domain.ModSuper == 0 {
		t.Fatal(events, err)
	}
	if _, err := i.Release("h"); err != nil {
		t.Fatal(err)
	}
	events, err = i.Release("Super")
	if err != nil || events[0].(ports.KeyEvent).Mods&domain.ModSuper != 0 {
		t.Fatal(events, err)
	}
	events, err = i.Press("Super+Shift+a")
	if err != nil || len(events) != 3 {
		t.Fatal(events, err)
	}
	events, err = i.Release("Super+Shift+a")
	if err != nil || len(events) != 3 {
		t.Fatal(events, err)
	}
	for n, code := range []uint32{30, 42, 125} {
		if ev := events[n].(ports.KeyEvent); ev.Pressed || ev.Keycode != code {
			t.Fatal(ev)
		}
	}
}

func TestInjectPointerAndSwipe(t *testing.T) {
	i := newInjector(injectKeymap(t), nil)
	if got := i.Device(); got != (ports.InputDeviceAdded{ID: controlDeviceID, Name: "control"}) {
		t.Fatal(got)
	}
	events, err := i.Pointer("move", []string{"12.5", "45"})
	if err != nil {
		t.Fatal(err)
	}
	m := events[0].(ports.PointerMotion)
	if m.Device != controlDeviceID || m.X != 12.5 || m.Y != 45 || m.DX != 0 || m.DY != 0 {
		t.Fatal(m)
	}
	for _, name := range []string{"left", "right", "middle"} {
		var args []string
		if name != "left" {
			args = []string{name}
		}
		events, err = i.Pointer("click", args)
		if err != nil || len(events) != 2 {
			t.Fatal(events, err)
		}
		down, up := events[0].(ports.PointerButton), events[1].(ports.PointerButton)
		code := map[string]uint32{"left": 0x110, "right": 0x111, "middle": 0x112}[name]
		if down.Device != controlDeviceID || down.Button != code || !down.Pressed || up.Pressed || up.Button != code || up.Time != down.Time {
			t.Fatal(events)
		}
	}
	for _, fingers := range []int{3, 4} {
		for dir, delta := range map[string][2]float64{"left": {-40, 0}, "right": {40, 0}, "up": {0, -40}, "down": {0, 40}} {
			events, err := i.Swipe(fingers, dir)
			if err != nil || len(events) != 12 {
				t.Fatal(events, err)
			}
			begin := events[0].(ports.SwipeBegin)
			if begin.Device != controlDeviceID || begin.Fingers != fingers {
				t.Fatal(begin)
			}
			at := begin.Time
			for _, raw := range events[1:11] {
				u := raw.(ports.SwipeUpdate)
				if u.Device != controlDeviceID || u.DX != delta[0] || u.DY != delta[1] || u.Time-at != 8*time.Millisecond {
					t.Fatal(u)
				}
				at = u.Time
			}
			if end := events[11].(ports.SwipeEnd); end.Device != controlDeviceID || end.Cancelled || end.Time != at {
				t.Fatal(end)
			}
		}
	}
}

func TestInjectInvalid(t *testing.T) {
	i := newInjector(injectKeymap(t), nil)
	for _, text := range []string{"", "Super+", "Unknown+a", "Super+not-a-key", "Super extra"} {
		for _, f := range []func(string) ([]ports.InputEvent, error){i.Key, i.Press, i.Release} {
			if ev, err := f(text); err == nil || len(ev) != 0 {
				t.Fatalf("%q: %v %v", text, ev, err)
			}
		}
	}
	if ev, err := i.Type("ok🔐"); err == nil || len(ev) != 0 {
		t.Fatal(ev, err)
	}
	for _, cmd := range []struct {
		name string
		args []string
	}{{"move", nil}, {"move", []string{"NaN", "1"}}, {"move", []string{"1", "Inf"}}, {"move", []string{"x", "2"}}, {"move", []string{"1", "2", "3"}}, {"click", []string{"bad"}}, {"down", nil}, {"up", []string{"left", "right"}}, {"bad", nil}} {
		if ev, err := i.Pointer(cmd.name, cmd.args); err == nil || len(ev) != 0 {
			t.Fatal(ev, err)
		}
	}
	for _, args := range []struct {
		f int
		d string
	}{{2, "left"}, {3, "sideways"}, {5, "up"}} {
		if ev, err := i.Swipe(args.f, args.d); err == nil || len(ev) != 0 {
			t.Fatal(ev, err)
		}
	}
	// Validation did not leave Super or an implicit Shift pressed.
	ev, err := i.Key("a")
	if err != nil || ev[0].(ports.KeyEvent).Mods != 0 {
		t.Fatal(ev, err)
	}
}

func TestInjectSecuritySnapshots(t *testing.T) {
	security := portsmocks.NewMockSessionSecurity(t)
	state := domain.SecurityState{Generation: 7, Protected: true}
	security.EXPECT().Snapshot().Return(state).Maybe()
	i := newInjector(injectKeymap(t), security)
	device := i.Device().(ports.SecurityInput)
	if device.State != state || device.Event != (ports.InputDeviceAdded{ID: controlDeviceID, Name: "control"}) {
		t.Fatal(device)
	}
	var events []ports.InputEvent
	key, err := i.Key("A")
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, key...)
	move, err := i.Pointer("move", []string{"10", "20"})
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, move...)
	click, err := i.Pointer("click", nil)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, click...)
	swipe, err := i.Swipe(3, "left")
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, swipe...)
	for _, raw := range events {
		ev, ok := raw.(ports.SecurityInput)
		if !ok || ev.State != state || reflect.ValueOf(ev.Event).FieldByName("Device").Uint() != uint64(controlDeviceID) {
			t.Fatal(raw)
		}
	}
}

func TestInjectSecurityQuarantine(t *testing.T) {
	km := injectKeymap(t)
	security := portsmocks.NewMockSessionSecurity(t)
	initial := domain.SecurityState{}
	locked := domain.SecurityState{Generation: 1, Protected: true}
	security.EXPECT().Snapshot().Return(initial).Once()
	i := newInjector(km, security)
	if _, err := i.Press("Super"); err != nil {
		t.Fatal(err)
	}
	security.EXPECT().Snapshot().Return(locked).Times(5)
	events, err := i.Key("q")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range events {
		if ev := raw.(ports.SecurityInput).Event.(ports.KeyEvent); ev.Mods&domain.ModSuper != 0 {
			t.Fatal(ev)
		}
	}
	events, err = i.Release("Super")
	if err != nil || len(events) != 0 {
		t.Fatal("held modifier was not quarantined", events, err)
	}
	events, err = i.Key("q")
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
}

func TestInjectPunctuation(t *testing.T) {
	i := newInjector(injectKeymap(t), nil)
	for r := range injectPunctuation {
		events, err := i.Type(string(r))
		if err != nil || len(events) < 2 {
			t.Fatalf("rune %q: %v %v", r, events, err)
		}
	}
	if events, err := i.Type(""); err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
}

func TestInjectSecuritySnapshotBeforeTranslation(t *testing.T) {
	km := injectKeymap(t)
	security := portsmocks.NewMockSessionSecurity(t)
	initial := domain.SecurityState{}
	locked := domain.SecurityState{Generation: 1, Protected: true}
	security.EXPECT().Snapshot().Return(initial).Once()
	i := newInjector(km, security)
	if _, err := i.Press("Super"); err != nil {
		t.Fatal(err)
	}
	// The epoch changes between the key's press and release. The release is
	// quarantined, rather than translated using the previous modifier state.
	security.EXPECT().Snapshot().Return(initial).Once()
	security.EXPECT().Snapshot().Return(locked).Once()
	events, err := i.Key("q")
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	ev := events[0].(ports.SecurityInput)
	if ev.State != initial || ev.Event.(ports.KeyEvent).Mods&domain.ModSuper == 0 {
		t.Fatal(ev)
	}
	security.EXPECT().Snapshot().Return(locked).Times(3)
	events, err = i.Release("Super")
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	events, err = i.Key("q")
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for _, raw := range events {
		ev := raw.(ports.SecurityInput)
		if ev.State != locked || ev.Event.(ports.KeyEvent).Mods&domain.ModSuper != 0 {
			t.Fatal(ev)
		}
	}
}
