package wayland

import (
	"bytes"
	"context"
	"github.com/bnema/nefertty/internal/ports"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/logging"
)

func TestWaylandInfo(t *testing.T) {
	tool, err := exec.LookPath("wayland-info")
	if err != nil {
		t.Skip("wayland-info not installed")
	}
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, OutputWidth: 1920, OutputHeight: 1080}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(2 * time.Second):
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

func TestWestonFrames(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, OutputWidth: 1920, OutputHeight: 1080}, Channels{}, logging.For(context.Background(), "wayland"))
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
	clientCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(clientCtx, tool)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName())
	var stderr strings.Builder
	cmd.Stderr = &stderr
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
		case <-time.After(2 * time.Second):
			t.Error("client did not exit")
		}
	})
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			t.Fatalf("client exited: %v: %s", err, stderr.String())
		case <-deadline:
			t.Fatalf("only %d frames; stderr: %s", s.frames, stderr.String())
		case <-tick.C:
			var n uint64
			s.display.Do(func() { n = s.frames })
			if n >= 10 {
				return
			}
		}
	}
}

// lifecycleServer starts a display with buffered command and event channels.
func lifecycleServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	s, err := New(Options{RuntimeDir: dir, OutputWidth: 1920, OutputHeight: 1080}, Channels{Events: events, Commands: commands}, logging.For(context.Background(), "wayland"))
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

func lifecycleClient(t *testing.T, tool string, s *Server, dir string, extra []string, args ...string) (*exec.Cmd, <-chan error, *lockedBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = append(append(os.Environ(), "XDG_RUNTIME_DIR="+dir, "WAYLAND_DISPLAY="+s.SocketName()), extra...)
	output := &lockedBuffer{}
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
		case <-time.After(2 * time.Second):
			t.Error("client hung")
		}
	})
	return cmd, done, output
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func waitEvent(t *testing.T, events <-chan ports.ClientEvent, duration time.Duration) ports.ClientEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(duration):
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
func unmapped(t *testing.T, events <-chan ports.ClientEvent, id ports.WindowID) {
	t.Helper()
	ev := waitEvent(t, events, 3*time.Second)
	w, ok := ev.(ports.WindowUnmapped)
	if !ok || w.ID != id {
		t.Fatalf("expected unmapped %d, got %#v", id, ev)
	}
}

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
	case <-time.After(5 * time.Second):
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
	_, _, output := lifecycleClient(t, tool, s, dir, []string{"WAYLAND_DEBUG=client"})
	w := mapped(t, events, 5*time.Second)
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 640, Height: 480, Activated: true}
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("missing configure(640, 480: %s", output.String())
		case <-tick.C:
			if strings.Contains(output.String(), "configure(640, 480") {
				return
			}
		}
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
	case <-time.After(10 * time.Second):
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

func TestUnbufferedEventsDoNotBlockDisplay(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-shm")
	if err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	dir := t.TempDir()
	events := make(chan ports.ClientEvent)
	s, err := New(Options{RuntimeDir: dir, OutputWidth: 1920, OutputHeight: 1080}, Channels{Events: events}, logging.For(context.Background(), "wayland"))
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
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run hung")
		}
	})
	lifecycleClient(t, tool, s, dir, nil)
	deadline := time.After(5 * time.Second)
	for {
		ready := make(chan bool, 1)
		go func() { ready <- s.display.Do(func() {}) }()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatal("display stopped")
			}
		case <-time.After(time.Second):
			t.Fatal("display blocked on event")
		}
		mappedOnDisplay := false
		if !s.display.Do(func() { mappedOnDisplay = len(s.windows) > 0 }) {
			t.Fatal("display stopped")
		}
		if mappedOnDisplay {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no mapping event")
		case <-time.After(10 * time.Millisecond):
		}
	}
	mapped(t, events, time.Second)
}
