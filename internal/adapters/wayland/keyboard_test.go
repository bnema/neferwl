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

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
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
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run hung")
		}
	})
	return s, events, commands, dir
}

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
	c.Context().Register(proxy)
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
	locked  []uint32 // locked modifiers of each modifiers event
}

func (p *eventsProxy) Dispatch(e *wlturbo.Event) {
	p.opcodes = append(p.opcodes, e.Opcode)
	if e.Opcode == uint16(wayland.KeyboardEventModifiers) {
		e.Uint32() // serial
		e.Uint32() // depressed
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
	seat, err := c.Registry().BindID(g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, seat)
	id := c.AllocateID()
	proxy := &eventsProxy{}
	proxy.SetID(id)
	c.Context().Register(proxy)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, id)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	proxy.opcodes, proxy.keymaps = nil, nil
	sync := func() {
		t.Helper()
		// Commands apply on the display goroutine; wait until it has run them.
		deadline := time.Now().Add(time.Second)
		for len(commands) > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
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
	c.Context().Register(late)
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
		commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: code, Pressed: true, TimeMsec: 1}}
		commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: code, TimeMsec: 2}}
	}
	enter := regexp.MustCompile(`wl_keyboard#[0-9]+\.enter\(`)
	key := regexp.MustCompile(`wl_keyboard#[0-9]+\.key\(`)
	deadline := time.After(4 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("foot exited: %v: %s", err, output.String())
		case <-deadline:
			t.Fatalf("keyboard events missing: %s", output.String())
		case <-tick.C:
			text := output.String()
			if enter.MatchString(text) && key.MatchString(text) {
				return
			}
		}
	}
}
