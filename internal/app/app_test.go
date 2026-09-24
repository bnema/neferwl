package app

import (
	"context"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/ports"
)

func TestHeadlessConfigReload(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[background]\ncolor='#000000'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	shots := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", Config: cfg, ConfigPath: path, NoTerminal: true, ScreenshotDir: shots})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	latest := filepath.Join(shots, "latest.png")
	waitColor := func(want color.RGBA, deadline time.Time) bool {
		for time.Now().Before(deadline) {
			f, err := os.Open(latest)
			if err == nil {
				img, decodeErr := png.Decode(f)
				f.Close()
				if decodeErr == nil {
					r, g, b, _ := img.At(1900, 10).RGBA()
					if uint8(r>>8) == want.R && uint8(g>>8) == want.G && uint8(b>>8) == want.B {
						return true
					}
				}
			}
			select {
			case err := <-done:
				if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
					t.Skipf("Vulkan unavailable: %v", err)
				}
				t.Fatalf("app exited early: %v", err)
			default:
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !waitColor(color.RGBA{A: 255}, time.Now().Add(10*time.Second)) {
		t.Fatal("first black frame missing")
	}
	if err := os.WriteFile(path, []byte("[background]\ncolor='#ff0000'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !waitColor(color.RGBA{R: 255, A: 255}, time.Now().Add(10*time.Second)) {
		t.Fatal("red frame missing after reload")
	}
}

func TestQuitJoinsWorkers(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), Options{Backend: "headless", Config: config.Defaults(), NoTerminal: true}, func(input chan<- ports.InputEvent) {
			input <- ports.KeyEvent{Keysym: "BackSpace", Mods: ports.ModCtrl | ports.ModAlt, Pressed: true}
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quit did not join workers")
	}
}

func TestHeadlessSpawnClose(t *testing.T) {
	if _, err := exec.LookPath("weston-simple-shm"); err != nil {
		t.Skip("weston-simple-shm not installed")
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Defaults()
	cfg.Terminal.Command = []string{"weston-simple-shm"}
	scenes := make(chan ports.Scene, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputReady := make(chan chan<- ports.InputEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Options{Backend: "headless", NoTerminal: true, Config: cfg, testScenes: scenes}, func(input chan<- ports.InputEvent) { inputReady <- input })
	}()
	var input chan<- ports.InputEvent
	select {
	case input = <-inputReady:
	case <-time.After(3 * time.Second):
		t.Fatal("app did not start")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	input <- ports.KeyEvent{Keysym: "Return", Mods: ports.ModSuper, Pressed: true}
	waitScene := func(count int, timeout time.Duration) {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case s := <-scenes:
				if len(s.Windows) == count {
					// Half of 1920 with the default zero gaps.
					if count == 1 && s.Windows[0].Rect.W != 960 {
						t.Errorf("width = %d, want 960", s.Windows[0].Rect.W)
					}
					return
				}
			case <-timer.C:
				t.Fatalf("no scene with %d windows", count)
			case err := <-done:
				t.Fatalf("app exited early: %v", err)
			}
		}
	}
	waitScene(1, 10*time.Second)
	input <- ports.KeyEvent{Keysym: "q", Mods: ports.ModSuper, Pressed: true}
	waitScene(0, 5*time.Second)
}

func TestHeadlessTyping(t *testing.T) {
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("foot unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Defaults()
	cfg.Terminal.Command = []string{"foot", "-c", "/dev/null", "sh"}
	dir := t.TempDir()
	err := Run(context.Background(), Options{Backend: "headless", Config: cfg, ScreenshotDir: dir, Script: io.NopCloser(strings.NewReader("sleep 1.5s\ntype echo nefertty-ok\nkey Return\nsleep 1s\n")), Timeout: 6 * time.Second})
	if err != nil {
		if strings.Contains(err.Error(), "Vulkan") || strings.Contains(err.Error(), "vulkan") {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Fatal(err)
	}
	path := filepath.Join(dir, "latest.png")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	// The default column occupies the central portion of the 1920x1080 output.
	first := img.At(600, 300)
	varied := false
	for y := 200; y < 850 && !varied; y += 4 {
		for x := 500; x < 1400; x += 4 {
			if img.At(x, y) != first {
				varied = true
				break
			}
		}
	}
	if !varied {
		t.Fatal("window image is uniform")
	}
	if os.Getenv("NEFERTTY_KEEP_SHOTS") == "1" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("/tmp/nefertty-typing.png", data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHeadlessPointerClickFocus(t *testing.T) {
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("foot unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Defaults()
	cfg.Terminal.Command = []string{"foot", "-c", "/dev/null", "sh"}
	scenes := make(chan ports.Scene, 128)
	err := Run(context.Background(), Options{Backend: "headless", Config: cfg, Script: io.NopCloser(strings.NewReader("sleep 1s\nkey Super+Return\nsleep 1s\nmove 600 300\nclick\nsleep 500ms\n")), Timeout: 5 * time.Second, testScenes: scenes})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "vulkan") {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Fatal(err)
	}
	two, focused := false, false
	for len(scenes) > 0 {
		s := <-scenes
		if len(s.Windows) == 2 {
			two = true
			for _, w := range s.Windows {
				if w.ID == 1 && w.Focused {
					focused = true
				}
			}
		}
	}
	if !two || !focused {
		t.Fatalf("two columns=%v first focused=%v", two, focused)
	}
}

func TestBlockedScriptShutdown(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	r, w := io.Pipe()
	defer w.Close()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Backend: "headless", Config: config.Defaults(), NoTerminal: true, Script: r, Timeout: 300 * time.Millisecond})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked script prevented shutdown")
	}
}
