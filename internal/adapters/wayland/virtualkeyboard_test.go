package wayland

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/virtualkeyboard"
	"github.com/bnema/purego-libwayland/protocol/wayland"
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
	if err := c.SendRequestWithFDs(kb, uint16(virtualkeyboard.ZwpVirtualKeyboardV1RequestKeymap), []int{fd}, uint32(wayland.KeyboardKeymapFormatXkbV1), size); err != nil {
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
	target.Context().Register(proxy)
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
	want := []uint16{uint16(wayland.KeyboardEventKeymap), uint16(wayland.KeyboardEventKey), uint16(wayland.KeyboardEventKey)}
	if !slices.Equal(proxy.opcodes, want) {
		t.Fatalf("virtual events %v, want %v", proxy.opcodes, want)
	}
	if len(proxy.keymaps) != 1 || proxy.keymaps[0] != virtual {
		t.Fatal("focused client did not get the virtual keymap")
	}

	proxy.opcodes, proxy.keymaps = nil, nil
	commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: 30, Pressed: true, TimeMsec: 3}}
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
	target.Context().Register(proxy)
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
	want := []uint16{k, m, key, key, k, m}
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
	target.Context().Register(late)
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
