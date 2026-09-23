package wayland

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
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

func keyboardServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: dir, OutputWidth: 1920, OutputHeight: 1080, Keymap: keymapText(t), RepeatRate: 25, RepeatDelay: 600}, Channels{Events: events, Commands: commands}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
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
	for _, want := range []string{"capabilities: keyboard", "keyboard repeat rate: 25", "keyboard repeat delay: 600"} {
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
