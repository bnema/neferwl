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

func TestVirtualKeyBeforeKeymap(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	typer, kb := virtualTyper(t, s, dir)
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	expectProtocolError(t, typer, kb, uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap))
}
