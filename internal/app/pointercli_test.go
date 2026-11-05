package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bnema.dev/wlturbo"
	"github.com/bnema/neferwl/internal/adapters/config"
)

// startPointerHeadless runs a headless NeferWL with one w x h output in an
// isolated runtime and returns its socket path.
//
//testpolicy:realtime real headless compositor startup and teardown
func startPointerHeadless(t *testing.T, w, h int) string {
	t.Helper()
	for _, k := range []string{"NEFERWL_STATE", "NEFERWL_SLOT", "WAYLAND_DISPLAY"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := config.Defaults()
	cfg.Animations.On = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, PlanOverrides: noTerminal(), Config: cfg, Sizes: [][2]int{{w, h}}})
	}()
	stopped := false
	t.Cleanup(func() {
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
	})
	socket := filepath.Join(runtime, "wayland-1")
	waitFile := fileChangesWithin(t, runtime, 5*time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			return socket
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
		if !waitFile() {
			t.Fatal("no wayland socket")
		}
	}
}

func buildNeferwl(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "neferwl")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/neferwl")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build neferwl: %v: %s", err, out)
	}
	return bin
}

// pointerCLI runs `neferwl pointer args...` against the headless display.
//
//testpolicy:realtime child process against the real headless compositor
func pointerCLI(t *testing.T, bin string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"pointer"}, args...)...)
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY=wayland-1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("neferwl pointer %v: %v: %s", args, err, out)
	}
}

// `neferwl pointer` clicks, drags and scrolls on a real client of a
// headless NeferWL; buttons it leaves pressed are released at exit.
func TestPointerCLI(t *testing.T) {
	bin := buildNeferwl(t)
	socket := startPointerHeadless(t, 320, 240)
	c, err := wlturbo.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := mapPointerTarget(t, c)
	dispatchUntil(t, c, 5*time.Second, func() string { return "no keyboard focus" }, func() bool { return target.focused })
	wait := func(what string, ok func() bool) {
		t.Helper()
		dispatchUntil(t, c, 5*time.Second, func() string { return fmt.Sprintf("%s: %+v", what, *target) }, ok)
	}

	// The only window fills the output: its centre is the output centre.
	pointerCLI(t, bin, "abs", "0.5", "0.5", "click")
	wait("click", func() bool { return target.entered && target.release })
	x0, y0 := target.x, target.y

	target.pressed, target.release = false, false
	pointerCLI(t, bin, "--drag-steps", "4", "--drag-step-delay", "1ms", "drag", "40", "-20")
	wait("drag", func() bool { return target.release })
	if target.motions < 4 || target.x != x0+40 || target.y != y0-20 {
		t.Fatalf("drag: %d motions to %v,%v from %v,%v", target.motions, target.x, target.y, x0, y0)
	}

	pointerCLI(t, bin, "scroll", "down", "3", "scroll", "left")
	wait("scroll", func() bool { return target.scrollV == 3 && target.scrollH == -1 })

	// A press left at exit is released when the pointer goes away.
	target.pressed, target.release = false, false
	pointerCLI(t, bin, "move", "-10", "0", "press", "right", "press")
	wait("release at exit", func() bool { return target.release })
	if got := target.buttons[len(target.buttons)-2:]; got[0] != 0x111 || got[1] != 0x110 {
		t.Fatalf("buttons %#x", target.buttons)
	}
}
