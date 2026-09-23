package wayland

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
