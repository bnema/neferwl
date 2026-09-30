package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/wlturbo"
	clientcore "github.com/bnema/wlturbo/protocol/core"
	clienttext "github.com/bnema/wlturbo/protocol/textinput"
	clientxdg "github.com/bnema/wlturbo/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

// lockedBuffer is an io.Writer read while exec writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// textClient retains observations from an ordinary generated text-input proxy.
type textClient struct {
	*clienttext.TextInputV3
	entered bool
	preedit []string
	commits []string
}

// NeferWL headless, the examples/testime input method and a text input
// client: the input method's preedit and commit reach the client.
func TestHeadlessTextInputIME(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ime := filepath.Join(t.TempDir(), "testime")
	build := exec.Command("go", "build", "-o", ime, "../../examples/testime")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build testime: %v: %s", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, NoTerminal: true, Config: config.Defaults()})
	}()
	stopped := false // Run's result was already received
	defer func() {
		cancel()
		if stopped {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	socket := filepath.Join(runtime, "wayland-1")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-done:
			stopped = true
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
				t.Skipf("Vulkan unavailable: %v", err)
			}
			t.Fatalf("neferwl exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no wayland socket")
		}
	}

	// Two commits: the second comes from enabling the text input again.
	cmd := exec.Command(ime, "-count", "2", "-preedit", "nihon", "-commit", "日本")
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY=wayland-1")
	imeOut := &lockedBuffer{}
	cmd.Stderr = imeOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	imeDone := make(chan error, 1)
	go func() { imeDone <- cmd.Wait() }()
	imeExited := false
	defer func() {
		if !imeExited {
			_ = cmd.Process.Kill()
			<-imeDone
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(imeOut.String(), "ready"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("testime not ready: %s", imeOut.String())
		}
	}

	c, err := wlturbo.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	text := mapTextClient(t, c)
	poll := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: preedit %q commits %q, testime: %s", what, text.preedit, text.commits, imeOut.String())
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
		}
	}
	poll("no text input enter", func() bool { return text.entered })
	for i := 1; i <= 2; i++ {
		if err := text.Enable(); err != nil {
			t.Fatal(err)
		}
		if err := text.SetContentType(0, 0); err != nil {
			t.Fatal(err)
		}
		if err := text.Commit(); err != nil {
			t.Fatal(err)
		}
		poll("missing commit", func() bool { return len(text.commits) == i })
	}
	if len(text.preedit) != 2 || text.preedit[0] != "nihon" || text.commits[0] != "日本" || text.commits[1] != "日本" {
		t.Fatalf("preedit %q commits %q", text.preedit, text.commits)
	}
	select {
	case err := <-imeDone:
		imeExited = true
		if err != nil {
			t.Fatalf("testime: %v: %s", err, imeOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("testime did not exit after two commits: %s", imeOut.String())
	}
}

// mapTextClient maps a 1x1 toplevel with a text input on c.
func mapTextClient(t *testing.T, c *wlturbo.Display) *textClient {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(c.Roundtrip())
	comp := clientcore.NewCompositor(c.Context())
	shm := clientcore.NewShm(c.Context())
	seat := clientcore.NewSeat(c.Context())
	tim := clienttext.NewTextInputManagerV3(c.Context())
	wm := clientxdg.NewXdgWmBase(c.Context())
	wm.OnPing(func(serial uint32) { check(wm.Pong(serial)) })
	bindTestClient(t, c, clientcore.CompositorInterface, 1, comp)
	bindTestClient(t, c, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, c, clientcore.SeatInterface, 1, seat)
	bindTestClient(t, c, clienttext.TextInputManagerV3Interface, 1, tim)
	bindTestClient(t, c, clientxdg.XdgWmBaseInterface, 1, wm)
	fd, err := unix.MemfdCreate("text-client", 0)
	check(err)
	if err := unix.Ftruncate(fd, 4); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	// CreatePool owns the sent descriptor on success; only close it on error.
	pool, err := shm.CreatePool(fd, 4)
	if err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	buffer, err := pool.CreateBuffer(0, 1, 1, 4, clientcore.FORMAT_ARGB8888)
	check(err)
	surf, err := comp.CreateSurface()
	check(err)
	xdg, err := wm.GetXdgSurface(surf)
	check(err)
	var serial uint32
	xdg.OnConfigure(func(value uint32) { serial = value })
	_, err = xdg.GetToplevel()
	check(err)
	proxy, err := tim.GetTextInput(seat)
	check(err)
	text := &textClient{TextInputV3: proxy}
	proxy.OnEnter(func(uint32) { text.entered = true })
	proxy.OnPreeditString(func(value string, _, _ int32) { text.preedit = append(text.preedit, value) })
	proxy.OnCommitString(func(value string) { text.commits = append(text.commits, value) })
	check(surf.Commit())
	for deadline := time.Now().Add(5 * time.Second); serial == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no configure")
		}
		check(c.Roundtrip())
	}
	check(xdg.AckConfigure(serial))
	check(surf.Attach(buffer, 0, 0))
	check(surf.Commit())
	return text
}
