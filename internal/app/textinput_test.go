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
	"github.com/bnema/purego-libwayland/protocol/textinput"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
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

// textClient records what a text input receives.
type textClient struct {
	wlturbo.BaseProxy
	entered bool
	preedit []string
	commits []string
}

func (p *textClient) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case textinput.ZwpTextInputV3EventEnter:
		p.entered = true
	case textinput.ZwpTextInputV3EventPreeditString:
		p.preedit = append(p.preedit, e.String())
	case textinput.ZwpTextInputV3EventCommitString:
		p.commits = append(p.commits, e.String())
	}
}

// xdgConfigure records the last configure serial.
type xdgConfigure struct {
	wlturbo.BaseProxy
	serial uint32
}

func (p *xdgConfigure) Dispatch(e *wlturbo.Event) {
	if uint32(e.Opcode) == xdgshell.SurfaceEventConfigure {
		p.serial = e.Uint32()
	}
}

// quiet ignores the events of objects the test does not inspect.
type quiet struct{ wlturbo.BaseProxy }

func (*quiet) Dispatch(*wlturbo.Event) {}

type wmPing struct {
	wlturbo.BaseProxy
	c *wlturbo.Display
}

func (p *wmPing) Dispatch(e *wlturbo.Event) {
	if uint32(e.Opcode) == xdgshell.WmBaseEventPing {
		_ = p.c.SendRequest(p.ID(), uint16(xdgshell.WmBaseRequestPong), e.Uint32())
	}
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
	send := func(op uint32, args ...any) {
		t.Helper()
		if err := c.SendRequest(text.ID(), uint16(op), args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 2; i++ {
		send(textinput.ZwpTextInputV3RequestEnable)
		send(textinput.ZwpTextInputV3RequestSetContentType, uint32(0), uint32(0))
		send(textinput.ZwpTextInputV3RequestCommit)
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
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	bind := func(iface string) uint32 {
		t.Helper()
		g, ok := c.Registry().FindGlobal(iface)
		if !ok {
			t.Fatalf("missing %s", iface)
		}
		id, err := c.Registry().BindID(g.Name, g.Interface, 1)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// New IDs are used in allocation order; every object gets a proxy.
	newID := func() uint32 {
		p := &quiet{}
		p.SetID(c.AllocateID())
		c.Context().Register(p)
		return p.ID()
	}
	req := func(id, op uint32, args ...any) {
		t.Helper()
		if err := c.SendRequest(id, uint16(op), args...); err != nil {
			t.Fatal(err)
		}
	}
	comp, shm, seat, tim := bind("wl_compositor"), bind("wl_shm"), bind("wl_seat"), bind("zwp_text_input_manager_v3")
	for _, id := range []uint32{comp, shm, seat, tim} {
		p := &quiet{}
		p.SetID(id)
		c.Context().Register(p)
	}
	wm := &wmPing{c: c}
	wm.SetID(bind("xdg_wm_base"))
	c.Context().Register(wm)
	fd, err := unix.MemfdCreate("text-client", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool := newID()
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	buffer := newID()
	req(pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	surf := newID()
	req(comp, wayland.CompositorRequestCreateSurface, surf)
	xdg := &xdgConfigure{}
	xdg.SetID(c.AllocateID())
	c.Context().Register(xdg)
	req(wm.ID(), xdgshell.WmBaseRequestGetXdgSurface, xdg.ID(), surf)
	top := newID()
	req(xdg.ID(), xdgshell.SurfaceRequestGetToplevel, top)
	text := &textClient{}
	text.SetID(c.AllocateID())
	c.Context().Register(text)
	req(tim, textinput.ZwpTextInputManagerV3RequestGetTextInput, text.ID(), seat)
	req(surf, wayland.SurfaceRequestCommit)
	for deadline := time.Now().Add(5 * time.Second); xdg.serial == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no configure")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	req(xdg.ID(), xdgshell.SurfaceRequestAckConfigure, xdg.serial)
	req(surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	req(surf, wayland.SurfaceRequestCommit)
	return text
}
