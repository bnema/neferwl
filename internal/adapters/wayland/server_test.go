package wayland

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestWaylandInfo(t *testing.T) {
	tool, err := exec.LookPath("wayland-info")
	if err != nil {
		t.Skip("wayland-info not installed")
	}
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-waylandDeadline(2 * time.Second):
			t.Error("Run did not stop")
		}
		if _, err := os.Stat(filepath.Join(dir, s.SocketName())); !os.IsNotExist(err) {
			t.Errorf("socket remains: %v", err)
		}
	}()
	cmdCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	cmd := exec.CommandContext(cmdCtx, tool)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wayland-info: %v\n%s", err, out)
	}
	for _, want := range []string{"wl_compositor", "wl_shm", "wl_output", "wl_seat", "wl_data_device_manager", "HEADLESS-1"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

//
//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestWestonFrames(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
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
	clientCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(clientCtx, tool)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName(), "WAYLAND_DEBUG=client")
	stderr := &lockedBuffer{wrote: make(chan struct{}, 1)}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		stop()
		select {
		case <-exited:
		case <-waylandDeadline(2 * time.Second):
			t.Error("client did not exit")
		}
	})
	// The client logs each frame callback it receives (wl_callback#N.done).
	frame := regexp.MustCompile(`wl_callback#[0-9]+\.done\(`)
	if exited, ok := waitOutput(stderr, exited, 5*time.Second, func(text string) bool { return len(frame.FindAllStringIndex(text, -1)) >= 10 }); !ok {
		t.Fatalf("fewer than 10 frames (client exit: %v); stderr: %s", exited, stderr.String())
	}
}

// lifecycleServer starts a display with buffered command and event channels.
//
//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
func lifecycleServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, CaptureAllow: allowStore(t, "*\n")}, Channels{Events: events, Commands: commands}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
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
func lifecycleClient(t *testing.T, tool string, s *Server, dir string, extra []string, args ...string) (*exec.Cmd, <-chan error, *lockedBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = append(append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName()), extra...)
	output := &lockedBuffer{wrote: make(chan struct{}, 1)}
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { done <- cmd.Wait(); close(finished) }()
	t.Cleanup(func() {
		cancel()
		_ = cmd.Process.Kill()
		select {
		case <-finished:
		case <-waylandDeadline(2 * time.Second):
			t.Error("client hung")
		}
	})
	return cmd, done, output
}

type lockedBuffer struct {
	mu    sync.Mutex
	b     bytes.Buffer
	wrote chan struct{}
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.b.Write(p)
	if b.wrote != nil {
		select {
		case b.wrote <- struct{}{}:
		default:
		}
	}
	return n, err
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// waitOutput waits until ok accepts the buffer's text, the process exits
// (done) or one deadline passes; it returns the exit error and whether ok
// accepted. b must have been made with a wrote channel.
//
//testpolicy:realtime real external Wayland client process writing to a pipe
func waitOutput(b *lockedBuffer, done <-chan error, d time.Duration, ok func(string) bool) (exited error, accepted bool) {
	deadline := waylandDeadline(d)
	for !ok(b.String()) {
		select {
		case <-b.wrote:
		case err := <-done:
			if err == nil {
				err = errors.New("exit status 0")
			}
			return err, ok(b.String())
		case <-deadline:
			return nil, false
		}
	}
	return nil, true
}

//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func waitEvent(t *testing.T, events <-chan ports.ClientEvent, duration time.Duration) ports.ClientEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-waylandDeadline(duration):
		t.Fatal("event timeout")
		return nil
	}
}
func mapped(t *testing.T, events <-chan ports.ClientEvent, duration time.Duration) ports.WindowMapped {
	t.Helper()
	ev := waitEvent(t, events, duration)
	w, ok := ev.(ports.WindowMapped)
	if !ok {
		t.Fatalf("expected mapped, got %T", ev)
	}
	return w
}
func unmapped(t *testing.T, events <-chan ports.ClientEvent, id domain.WindowID) {
	t.Helper()
	ev := waitEvent(t, events, 3*time.Second)
	w, ok := ev.(ports.WindowUnmapped)
	if !ok || w.ID != id {
		t.Fatalf("expected unmapped %d, got %#v", id, ev)
	}
}

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestWindowClose(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	s, events, commands, dir := lifecycleServer(t)
	_, done, output := lifecycleClient(t, tool, s, dir, nil)
	w := mapped(t, events, 5*time.Second)
	commands <- ports.CloseWindow{ID: w.ID}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("client: %v: %s", err, output.String())
		}
	case <-waylandDeadline(5 * time.Second):
		t.Fatal("client did not close")
	}
	unmapped(t, events, w.ID)
}

func TestWindowConfigure(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	s, events, commands, dir := lifecycleServer(t)
	_, done, output := lifecycleClient(t, tool, s, dir, []string{"WAYLAND_DEBUG=client"})
	w := mapped(t, events, 5*time.Second)
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 640, Height: 480, Activated: true}
	if exited, ok := waitOutput(output, done, 3*time.Second, func(text string) bool { return strings.Contains(text, "configure(640, 480") }); !ok {
		t.Fatalf("missing configure(640, 480 (client exit: %v): %s", exited, output.String())
	}
}
func TestWindowKilled(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	s, events, _, dir := lifecycleServer(t)
	cmd, _, _ := lifecycleClient(t, tool, s, dir, nil)
	w := mapped(t, events, 5*time.Second)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	unmapped(t, events, w.ID)
}

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestFootWindow(t *testing.T) {
	tool, err := exec.LookPath("foot")
	if err != nil {
		t.Skip("foot not installed")
	}
	s, events, _, dir := lifecycleServer(t)
	_, done, output := lifecycleClient(t, tool, s, dir, []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, "-c", "/dev/null", "sh", "-c", "sleep 3")
	select {
	case ev := <-events:
		w, ok := ev.(ports.WindowMapped)
		if !ok {
			t.Fatalf("expected mapped, got %T", ev)
		}
		if w.AppID != "foot" {
			t.Fatalf("app ID: %q", w.AppID)
		}
	case err := <-done:
		t.Fatalf("foot exited: %v: %s", err, output.String())
	case <-waylandDeadline(10 * time.Second):
		t.Fatalf("foot did not map: %s", output.String())
	}
}

func TestNewMissingRuntimeDirClosesDisplay(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := New(Options{RuntimeDir: dir}, Channels{}, logging.For(context.Background(), "wayland")); err == nil {
		t.Fatal("expected error")
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("fd leak: %d -> %d", len(before), len(after))
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unexpected directory: %v", err)
	}
}

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestUnbufferedEventsDoNotBlockDisplay(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	dir := t.TempDir()
	events := make(chan ports.ClientEvent)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
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
				t.Error(err)
			}
		case <-waylandDeadline(2 * time.Second):
			t.Error("Run hung")
		}
	})
	lifecycleClient(t, tool, s, dir, nil)
	// The events channel is unbuffered and nobody receives yet: the display
	// must keep serving calls while the mapping event is undelivered.
	ready := make(chan bool, 1)
	go func() { ready <- s.display.Do(func() {}) }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("display stopped")
		}
	case <-waylandDeadline(5 * time.Second):
		t.Fatal("display blocked on event")
	}
	mapped(t, events, 5*time.Second)
	mappedOnDisplay := false
	if !s.display.Do(func() { mappedOnDisplay = len(s.windows) > 0 }) {
		t.Fatal("display stopped")
	}
	if !mappedOnDisplay {
		t.Fatal("mapping event delivered without a window on the display")
	}
}

//testpolicy:realtime real external Wayland client process on the display socket (Server.Run on libwayland epoll)
func TestWestonContents(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	contents := relayContents(t, s)
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
	_, _, output := lifecycleClient(t, tool, s, dir, nil)
	w := mapped(t, events, 5*time.Second)
	first := waylandDeadline(5 * time.Second)
	count := 0
	for count < 2 {
		select {
		case c := <-contents:
			if c.ID != w.ID || c.Width <= 0 || c.Height <= 0 || c.SHM == nil || c.SHM.Stride < c.Width*4 || c.SHM.File == nil {
				t.Fatalf("invalid content: %+v", c)
			}
			// The pool holds the client's pixels: read them like a renderer.
			px := make([]byte, c.SHM.Stride)
			if _, err := c.SHM.File.ReadAt(px, int64(c.SHM.Offset+c.Height/2*c.SHM.Stride)); err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(px, func(b byte) bool { return b != 0 }) {
				t.Fatal("empty pixels")
			}
			if count == 0 {
				first = waylandDeadline(2 * time.Second)
			}
			count++
		case <-first:
			t.Fatalf("only %d contents: %s", count, output.String())
		}
	}
}

// testOutputs is the 1920x1080 output tests start with.
var testOutputs = ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 1920, Height: 1080}, Width: 1920, Height: 1080, Scale: 1}}
