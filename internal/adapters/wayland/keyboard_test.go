package wayland

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

func keymapText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/us.xkb")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// keyboardServer runs a server; setup, if any, runs before Run starts.
//
//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
func keyboardServer(t *testing.T, setup ...func(*Server)) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Keymap: keymapText(t), RepeatRate: 25, RepeatDelay: 600}, Channels{Events: events, Commands: commands}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range setup {
		f(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-waylandDeadline(2 * time.Second):
			t.Error("Run hung")
		}
	})
	return s, events, commands, dir
}

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestKeyboardInfo(t *testing.T) {
	tool, err := exec.LookPath("wayland-info")
	if err != nil {
		t.Skip("wayland-info not installed")
	}
	s, _, _, dir := keyboardServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wayland-info: %v: %s", err, out)
	}
	for _, want := range []string{"capabilities: pointer keyboard", "keyboard repeat rate: 25", "keyboard repeat delay: 600"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

type keymapProxy struct {
	wlturbo.BaseProxy
	result chan error
}

func (p *keymapProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode != uint16(wayland.KeyboardEventKeymap) {
		return
	}
	format := e.Uint32()
	fd := int(e.Fd())
	size := e.Uint32()
	if fd <= 0 || size == 0 || format != uint32(wayland.KeyboardKeymapFormatXkbV1) {
		p.result <- os.ErrInvalid
		return
	}
	defer unix.Close(fd)
	data := make([]byte, size)
	_, err := unix.Pread(fd, data, 0)
	if err == nil && !strings.HasPrefix(string(data), "xkb_keymap") {
		err = os.ErrInvalid
	}
	p.result <- err
}
func TestKeyboardKeymapProtocol(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	id := c.AllocateID()
	proxy := &keymapProxy{result: make(chan error, 1)}
	proxy.SetID(id)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, id)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-proxy.result:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("missing keymap event")
	}
}

// events records keyboard event opcodes; a keymap event also checks its content.
type eventsProxy struct {
	wlturbo.BaseProxy
	opcodes []uint16
	keymaps []string
	locked  []uint32      // locked modifiers of each modifiers event
	depress []uint32      // depressed modifiers of each modifiers event
	repeat  chan struct{} // optional protocol barrier notification
}

func (p *eventsProxy) Dispatch(e *wlturbo.Event) {
	p.opcodes = append(p.opcodes, e.Opcode)
	if e.Opcode == uint16(wayland.KeyboardEventRepeatInfo) && p.repeat != nil {
		p.repeat <- struct{}{}
	}
	if e.Opcode == uint16(wayland.KeyboardEventModifiers) {
		e.Uint32() // serial
		p.depress = append(p.depress, e.Uint32())
		e.Uint32() // latched
		p.locked = append(p.locked, e.Uint32())
		return
	}
	if e.Opcode != uint16(wayland.KeyboardEventKeymap) {
		return
	}
	e.Uint32()
	fd := int(e.Fd())
	size := e.Uint32()
	defer unix.Close(fd)
	data := make([]byte, size)
	if _, err := unix.Pread(fd, data, 0); err == nil {
		p.keymaps = append(p.keymaps, strings.TrimRight(string(data), "\x00"))
	}
}

func TestSetKeymap(t *testing.T) {
	s, _, commands, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	// Version 4 so the keyboard receives repeat_info.
	g, ok := c.Registry().FindGlobal("wl_seat")
	if !ok {
		t.Fatal("missing wl_seat")
	}
	seat, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, seat)
	id := c.AllocateID()
	proxy := &eventsProxy{}
	proxy.SetID(id)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, id)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	proxy.opcodes, proxy.keymaps = nil, nil
	sync := func() {
		t.Helper()
		// Both updates end in repeat_info, after any keymap event.
		dispatchUntil(t, c, func() bool { return slices.Contains(proxy.opcodes, uint16(wayland.KeyboardEventRepeatInfo)) })
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}

	// Repeat-only: one repeat_info, no keymap.
	commands <- ports.SetKeymap{RepeatRate: 40, RepeatDelay: 300}
	sync()
	if len(proxy.opcodes) != 1 || proxy.opcodes[0] != uint16(wayland.KeyboardEventRepeatInfo) {
		t.Fatalf("repeat-only events %v", proxy.opcodes)
	}

	// New keymap: keymap then repeat_info, and the new content.
	proxy.opcodes = nil
	next := strings.Replace(keymapText(t), "xkb_keymap", "xkb_keymap ", 1)
	commands <- ports.SetKeymap{Keymap: next, RepeatRate: 40, RepeatDelay: 300}
	sync()
	want := []uint16{uint16(wayland.KeyboardEventKeymap), uint16(wayland.KeyboardEventRepeatInfo)}
	if !slices.Equal(proxy.opcodes, want) {
		t.Fatalf("keymap events %v, want %v", proxy.opcodes, want)
	}
	if len(proxy.keymaps) != 1 || proxy.keymaps[0] != next {
		t.Fatalf("keymap content not replaced (%d keymaps)", len(proxy.keymaps))
	}
	// A keyboard created afterwards gets the new keymap.
	id2 := c.AllocateID()
	late := &eventsProxy{}
	late.SetID(id2)
	registerWireProxy(c, late)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, id2)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(late.keymaps) != 1 || late.keymaps[0] != next {
		t.Fatal("late keyboard got the old keymap")
	}
}

func TestFootKeyboard(t *testing.T) {
	tool, err := exec.LookPath("foot")
	if err != nil {
		t.Skip("foot not installed")
	}
	s, events, commands, dir := keyboardServer(t)
	_, done, output := lifecycleClient(t, tool, s, dir, []string{"HOME=" + t.TempDir(), "WAYLAND_DEBUG=client"}, "-c", "/dev/null", "sh", "-c", "sleep 10")
	w := mapped(t, events, 10*time.Second)
	commands <- ports.FocusWindow{ID: w.ID}
	for _, code := range []uint32{18, 46, 35, 24, 57, 35, 23, 28} {
		commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: code, Pressed: true, Time: 1 * time.Millisecond}}
		commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: code, Time: 2 * time.Millisecond}}
	}
	enter := regexp.MustCompile(`wl_keyboard#[0-9]+\.enter\(`)
	key := regexp.MustCompile(`wl_keyboard#[0-9]+\.key\(`)
	if exited, ok := waitOutput(output, done, 4*time.Second, func(text string) bool { return enter.MatchString(text) && key.MatchString(text) }); !ok {
		t.Fatalf("keyboard events missing (foot exit: %v): %s", exited, output.String())
	}
}

// SetModifiers sends the requested depressed mask with the real latched and
// locked state, and Restore sends the real state back; the seat's own state
// is untouched. A window that is not focused is ignored.
func TestSetModifiersOverrideAndRestore(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	const capsLocked = 2
	var id domain.WindowID
	set := make(chan struct{})
	s.display.Do(func() { s.seat.modState = domain.ModState{Locked: capsLocked}; id = s.seat.focused; close(set) })
	<-set
	wait := func() {
		t.Helper()
		syncCommands(t, s, commands, target)
	}

	proxy.depress, proxy.locked = nil, nil
	commands <- ports.SetModifiers{ID: id + 100, Mods: domain.ModCtrl}
	wait()
	if len(proxy.depress) != 0 {
		t.Fatalf("unfocused window got modifiers %v", proxy.depress)
	}
	commands <- ports.SetModifiers{ID: id, Mods: domain.ModCtrl | domain.ModShift}
	wait()
	if !slices.Equal(proxy.depress, []uint32{xkbControl | xkbShift}) || !slices.Equal(proxy.locked, []uint32{capsLocked}) {
		t.Fatalf("override depressed %v locked %v", proxy.depress, proxy.locked)
	}
	done := make(chan domain.ModState, 1)
	commands <- ports.SetModifiers{ID: id, Restore: true, State: domain.ModState{Locked: capsLocked}}
	wait()
	if !slices.Equal(proxy.depress, []uint32{xkbControl | xkbShift, 0}) || !slices.Equal(proxy.locked, []uint32{capsLocked, capsLocked}) {
		t.Fatalf("restore depressed %v locked %v", proxy.depress, proxy.locked)
	}
	s.display.Do(func() { done <- s.seat.modState })
	if m := <-done; m != (domain.ModState{Locked: capsLocked}) {
		t.Fatalf("seat state changed: %+v", m)
	}
}

// Restore carries the real state core saw, which swallowed keys (a latch a
// hint label key consumed) never gave the seat: it is sent and adopted.
func TestSetModifiersRestoreAdoptsCoreState(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	target, proxy := focusedTarget(t, s, events, commands, dir)
	const shiftLatched, capsLocked = 1, 2
	var id domain.WindowID
	set := make(chan struct{})
	s.display.Do(func() { s.seat.modState = domain.ModState{Latched: shiftLatched}; id = s.seat.focused; close(set) })
	<-set
	proxy.depress, proxy.locked = nil, nil
	want := domain.ModState{Locked: capsLocked} // the latch was consumed
	commands <- ports.SetModifiers{ID: id, Restore: true, State: want}
	dispatchUntil(t, target, func() bool { return len(proxy.locked) > 0 })
	var got domain.ModState
	s.display.Do(func() { got = s.seat.modState })
	if got != want {
		t.Fatalf("seat state %+v, want %+v", got, want)
	}
	roundtrip(t, target)
	if !slices.Equal(proxy.locked, []uint32{capsLocked}) || !slices.Equal(proxy.depress, []uint32{0}) {
		t.Fatalf("restore depressed %v locked %v", proxy.depress, proxy.locked)
	}
}

// syncCommands observes an existing command through the real protocol. A
// repeat_info emitted by SetKeymap means all preceding commands were applied;
// merely emptying the channel or posting a display callback cannot prove that.
func syncCommands(t *testing.T, s *Server, commands chan<- ports.ClientCommand, c *wlturbo.Display) {
	t.Helper()
	var needsKeymap bool
	if !s.display.Do(func() { needsKeymap = s.seat.keymapFD < 0 }) {
		t.Fatal("display stopped")
	}
	if needsKeymap {
		keymap := keymapText(t)
		if !s.display.Do(func() { s.setKeymap(ports.SetKeymap{Keymap: keymap}) }) {
			t.Fatal("display stopped")
		}
	}
	seat := bindVersion(t, c, "wl_seat", 4)
	registerProtocol(t, c, seat)
	id := c.AllocateID()
	p := &eventsProxy{repeat: make(chan struct{}, 2)}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, id)
	roundtrip(t, c)
	select {
	case <-p.repeat:
	default:
		t.Fatal("missing initial repeat_info")
	}
	var rate, delay int
	if !s.display.Do(func() { rate, delay = s.seat.repeatRate, s.seat.repeatDelay }) {
		t.Fatal("display stopped")
	}
	commands <- ports.SetKeymap{RepeatRate: rate, RepeatDelay: delay}
	// Dispatch blocks on the protocol socket; close it on timeout to bound
	// failure without polling the display or sleeping for command delivery.
	dispatchUntil(t, c, func() bool { return len(p.repeat) > 0 })
	<-p.repeat
	requestProtocol(t, c, id, wayland.KeyboardRequestRelease)
	roundtrip(t, c)
}
