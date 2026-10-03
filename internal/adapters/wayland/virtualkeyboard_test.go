package wayland

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/virtualkeyboard"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// virtualTyper binds a virtual keyboard on its own connection, as wtype does.
func virtualTyper(t *testing.T, s *Server, dir string) (*wlturbo.Display, uint32) {
	t.Helper()
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	manager := bindProtocol(t, c, "zwp_virtual_keyboard_manager_v1")
	registerProtocol(t, c, manager)
	kb := c.AllocateID()
	registerProtocol(t, c, kb)
	requestProtocol(t, c, manager, virtualkeyboard.ZwpVirtualKeyboardManagerV1RequestCreateVirtualKeyboard, seat, kb)
	return c, kb
}

func sendVirtualKeymap(t *testing.T, c *wlturbo.Display, kb uint32, keymap string) {
	t.Helper()
	fd, size, err := keymapFile(keymap)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := wireRequest(c, kb, uint16(virtualkeyboard.ZwpVirtualKeyboardV1RequestKeymap), []int{fd}, uint32(wayland.KeyboardKeymapFormatXkbV1), size); err != nil {
		t.Fatal(err)
	}
}

// Virtual keys reach the focused client under the virtual keymap; the next
// real key switches it back to the seat keymap.
func TestVirtualKeyboardTypesIntoFocus(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target := protocolClient(t, s, dir)
	seat := bindProtocol(t, target, "wl_seat")
	registerProtocol(t, target, seat)
	proxy := &eventsProxy{}
	proxy.SetID(target.AllocateID())
	registerWireProxy(target, proxy)
	requestProtocol(t, target, seat, wayland.SeatRequestGetKeyboard, proxy.ID())
	w := toplevelMapper(t, target, events)()
	commands <- ports.FocusWindow{ID: w.ID}
	waitFocus(t, s, w.ID)
	if err := target.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	proxy.opcodes, proxy.keymaps = nil, nil

	typer, kb := virtualTyper(t, s, dir)
	virtual := strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap  ", 1)
	sendVirtualKeymap(t, typer, kb, virtual)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(30), uint32(0))
	if err := typer.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := target.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	want := []uint16{uint16(wayland.KeyboardEventKeymap), uint16(wayland.KeyboardEventModifiers), uint16(wayland.KeyboardEventKey), uint16(wayland.KeyboardEventKey)}
	if !slices.Equal(proxy.opcodes, want) {
		t.Fatalf("virtual events %v, want %v", proxy.opcodes, want)
	}
	if len(proxy.keymaps) != 1 || proxy.keymaps[0] != virtual {
		t.Fatal("focused client did not get the virtual keymap")
	}

	proxy.opcodes, proxy.keymaps = nil, nil
	commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: 30, Pressed: true, Time: 3 * time.Millisecond}}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(proxy.opcodes, uint16(wayland.KeyboardEventKey)) && time.Now().Before(deadline) {
		if err := target.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	want = []uint16{uint16(wayland.KeyboardEventKeymap), uint16(wayland.KeyboardEventModifiers), uint16(wayland.KeyboardEventKey)}
	if !slices.Equal(proxy.opcodes, want) {
		t.Fatalf("real key events %v, want %v", proxy.opcodes, want)
	}
	if len(proxy.keymaps) != 1 || proxy.keymaps[0] != keymapText(t) {
		t.Fatal("seat keymap not restored")
	}
}

// focusedTarget maps a focused toplevel with one keyboard and returns its
// connection and recorder, drained of setup events.
func focusedTarget(t *testing.T, s *Server, events chan ports.ClientEvent, commands chan ports.ClientCommand, dir string) (*wlturbo.Display, *eventsProxy) {
	t.Helper()
	target := protocolClient(t, s, dir)
	seat := bindProtocol(t, target, "wl_seat")
	registerProtocol(t, target, seat)
	proxy := &eventsProxy{}
	proxy.SetID(target.AllocateID())
	registerWireProxy(target, proxy)
	requestProtocol(t, target, seat, wayland.SeatRequestGetKeyboard, proxy.ID())
	w := toplevelMapper(t, target, events)()
	commands <- ports.FocusWindow{ID: w.ID}
	waitFocus(t, s, w.ID)
	if err := target.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	proxy.opcodes, proxy.keymaps = nil, nil
	return target, proxy
}

func roundtrip(t *testing.T, cs ...*wlturbo.Display) {
	t.Helper()
	for _, c := range cs {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
}

// A typer dying mid-key releases the key and restores the seat keymap.
func TestVirtualKeyboardDisconnectReleases(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	typer, kb := virtualTyper(t, s, dir)
	sendVirtualKeymap(t, typer, kb, strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap  ", 1))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestModifiers, uint32(1), uint32(0), uint32(0), uint32(0))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	roundtrip(t, typer)
	_ = typer.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(proxy.keymaps) < 2 && time.Now().Before(deadline) {
		roundtrip(t, target)
		time.Sleep(5 * time.Millisecond)
	}
	k, m := uint16(wayland.KeyboardEventKeymap), uint16(wayland.KeyboardEventModifiers)
	key := uint16(wayland.KeyboardEventKey)
	// The Modifiers request comes after the switch's own modifiers event.
	want := []uint16{k, m, m, key, key, k, m}
	if !slices.Equal(proxy.opcodes, want) {
		t.Fatalf("events %v, want %v", proxy.opcodes, want)
	}
	if proxy.keymaps[1] != keymapText(t) {
		t.Fatal("seat keymap not restored")
	}
}

// A keyboard created while a typer owns the keymap starts with it; a
// malformed keymap is ignored and the last good one stays.
func TestVirtualKeymapLateKeyboardAndRejected(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, _ := focusedTarget(t, s, events, commands, dir)
	typer, kb := virtualTyper(t, s, dir)
	virtual := strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap  ", 1)
	sendVirtualKeymap(t, typer, kb, virtual)
	sendVirtualKeymap(t, typer, kb, "not a keymap")
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	roundtrip(t, typer, target)
	seat := bindProtocol(t, target, "wl_seat")
	registerProtocol(t, target, seat)
	late := &eventsProxy{}
	late.SetID(target.AllocateID())
	registerWireProxy(target, late)
	requestProtocol(t, target, seat, wayland.SeatRequestGetKeyboard, late.ID())
	roundtrip(t, target)
	if len(late.keymaps) != 1 || late.keymaps[0] != virtual {
		t.Fatal("late keyboard did not get the virtual keymap")
	}
}

func TestVirtualKeyBeforeKeymap(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	typer, kb := virtualTyper(t, s, dir)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	expectProtocolError(t, typer, kb, uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap))
}

// A virtual keyboard with the seat keymap (dictation tools, wtype) sends
// no keymap to clients: only its keys, then no keymap on the switch back.
func TestVirtualKeyboardSameKeymapNotResent(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	for range 2 { // one typer per use, like a dictation app
		typer, kb := virtualTyper(t, s, dir)
		sendVirtualKeymap(t, typer, kb, keymapText(t))
		requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
		requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(30), uint32(0))
		roundtrip(t, typer, target)
		_ = typer.Close()
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		roundtrip(t, target)
		time.Sleep(5 * time.Millisecond)
	}
	if len(proxy.keymaps) != 0 {
		t.Fatalf("%d keymaps sent for a virtual keyboard with the seat keymap", len(proxy.keymaps))
	}
	keys := 0
	for _, o := range proxy.opcodes {
		if o == uint16(wayland.KeyboardEventKey) {
			keys++
		}
	}
	if keys != 4 {
		t.Fatalf("keys %d, want 4 (events %v)", keys, proxy.opcodes)
	}
}

// A virtual keyboard with the seat keymap types without the seat's
// modifiers: its switch sends its own (none), and the seat's come back
// with the next real key.
func TestVirtualKeyboardSameKeymapOwnModifiers(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	const capsLocked = 2
	set := make(chan struct{})
	s.display.Do(func() { s.seat.modState = ports.ModState{Locked: capsLocked}; close(set) })
	<-set
	proxy.locked = nil
	typer, kb := virtualTyper(t, s, dir)
	sendVirtualKeymap(t, typer, kb, keymapText(t))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	roundtrip(t, typer, target)
	m, key := uint16(wayland.KeyboardEventModifiers), uint16(wayland.KeyboardEventKey)
	if !slices.Equal(proxy.opcodes, []uint16{m, key}) || !slices.Equal(proxy.locked, []uint32{0}) {
		t.Fatalf("events %v locked %v, want modifiers with nothing locked, then the key", proxy.opcodes, proxy.locked)
	}
}

// A virtual keymap that differs from the seat's is sent on switch, on
// update while it owns the keyboards, and the seat's again on switch back.
func TestVirtualKeyboardOtherKeymapSent(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	typer, kb := virtualTyper(t, s, dir)
	first := strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap  ", 1)
	second := strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap   ", 1)
	sendVirtualKeymap(t, typer, kb, first)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	sendVirtualKeymap(t, typer, kb, second)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(30), uint32(0))
	roundtrip(t, typer, target)
	_ = typer.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(proxy.keymaps) < 3 && time.Now().Before(deadline) {
		roundtrip(t, target)
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Equal(proxy.keymaps, []string{first, second, keymapText(t)}) {
		t.Fatalf("keymaps %d, want virtual, updated virtual, seat", len(proxy.keymaps))
	}
}

// smallKeymap is a dictation tool keymap: only the keys it types.
const smallKeymap = `xkb_keymap {
xkb_keycodes "(unnamed)" {
minimum = 8;
maximum = 26;
<K1> = 9;
};
xkb_types "(unnamed)" { include "complete" };
xkb_compatibility "(unnamed)" { include "complete" };
xkb_symbols "(unnamed)" {
key <K1> {[D]};
};
};
`

func TestWidenKeycodes(t *testing.T) {
	const tail = "\nxkb_symbols { key <K1> {[D]}; };\n};\n"
	cases := []struct {
		name, in, want string
		from           int
	}{
		{"small maximum", smallKeymap, strings.Replace(smallKeymap, "maximum = 26;", "maximum = 255;", 1), 26},
		{"maximum 127..254", "xkb_keymap {\nxkb_keycodes { minimum = 8; maximum = 200; <K1> = 9; };" + tail,
			"xkb_keymap {\nxkb_keycodes { minimum = 8; maximum = 255; <K1> = 9; };" + tail, 200},
		{"no maximum", "xkb_keymap {\nxkb_keycodes \"x\" { minimum = 8; <K1> = 9; <K2> = 25; };" + tail,
			"xkb_keymap {\nxkb_keycodes \"x\" { maximum = 255; minimum = 8; <K1> = 9; <K2> = 25; };" + tail, 25},
		{"mixed case", "XKB_Keymap {\nXkb_KeyCodes { MINIMUM = 8; Maximum = 26; <K1> = 9; };" + tail,
			"XKB_Keymap {\nXkb_KeyCodes { MINIMUM = 8; Maximum = 255; <K1> = 9; };" + tail, 26},
		{"brace in comment", "xkb_keymap {\nxkb_keycodes {\n// ends here }\n# and } here\nminimum = 8;\nmaximum = 26;\n<K1> = 9;\n};" + tail,
			"xkb_keymap {\nxkb_keycodes {\n// ends here }\n# and } here\nminimum = 8;\nmaximum = 255;\n<K1> = 9;\n};" + tail, 26},
		{"commented maximum", "xkb_keymap {\nxkb_keycodes {\n// maximum = 26;\nminimum = 8;\n<K1> = 9;\n};" + tail,
			"xkb_keymap {\nxkb_keycodes { maximum = 255;\n// maximum = 26;\nminimum = 8;\n<K1> = 9;\n};" + tail, 9},
		{"maximum after comment", "xkb_keymap {\nxkb_keycodes {\nminimum = 8;\nmaximum = // why\n 26;\n<K1> = 9;\n};" + tail,
			"xkb_keymap {\nxkb_keycodes {\nminimum = 8;\nmaximum = // why\n 255;\n<K1> = 9;\n};" + tail, 26},
	}
	for _, c := range cases {
		got, from := widenKeycodes(c.in)
		if got != c.want || from != c.from {
			t.Errorf("%s: from %d want %d\n%s", c.name, from, c.from, got)
		}
	}
	for name, keep := range map[string]string{
		"seat keymap":       keymapText(t),
		"maximum 255":       "xkb_keymap { xkb_keycodes { maximum = 255; <K1> = 9; }; };",
		"maximum above 255": "xkb_keymap { xkb_keycodes { minimum = 8; maximum = 708; <K1> = 9; }; };",
		"no keycodes":       "xkb_keymap { xkb_symbols { }; };",
		"includes":          "xkb_keymap { xkb_keycodes { include \"evdev+aliases(qwerty)\" }; };",
		"maximum in string": "xkb_keymap { xkb_keycodes \"maximum = 26;\" { minimum = 8; <K1> = 300; }; };",
	} {
		if got, from := widenKeycodes(keep); from != 0 || got != keep {
			t.Errorf("%s changed:\n%s", name, got)
		}
	}
}

// Xwayland overflows its key maps on a keymap whose maximum keycode is
// small: clients get the virtual keymap with the full keycode range.
func TestVirtualKeymapWidenedForClients(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	typer, kb := virtualTyper(t, s, dir)
	sendVirtualKeymap(t, typer, kb, smallKeymap)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(1), uint32(1))
	roundtrip(t, typer, target)
	deadline := time.Now().Add(2 * time.Second)
	for len(proxy.keymaps) < 1 && time.Now().Before(deadline) {
		roundtrip(t, target)
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(proxy.keymaps); n == 0 || !strings.Contains(proxy.keymaps[n-1], "maximum = 255;") || strings.Contains(proxy.keymaps[n-1], "maximum = 26;") {
		t.Fatalf("keymaps %q", proxy.keymaps)
	}
}
