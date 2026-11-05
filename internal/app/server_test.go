package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/sessiondir"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// TestServerVirtualOutput: the persistent headless profile with one virtual output and no
// physical one focuses the virtual output at startup, and a client maps on
// it. Real libinput never runs (noLibinput); the inject channel is the input.
//
//testpolicy:realtime real Wayland client process, headless renderer and state file
func TestServerVirtualOutput(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Server mode always serves the session bus: keep it off the desktop's.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(runtimeDir, "no-bus"))
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("output.tv = virtual 640x360\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Terminal.Command = []string{os.Args[0], spawnClientArg}
	scenes := make(chan []domain.Scene, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputReady := make(chan chan<- ports.InputEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Options{Backend: domain.BackendHeadless, Profile: domain.ProfilePersistent, NoXwayland: true, Config: cfg, ConfigPath: path, testScenes: scenes, noLibinput: true},
			func(input chan<- ports.InputEvent) { inputReady <- input })
	}()
	var input chan<- ports.InputEvent
	select {
	case input = <-inputReady:
	case err := <-done:
		// Containers without /sys/class/drm or a GPU cannot run the persistent headless profile.
		if errors.Is(err, core.ErrNoRenderNode) || errors.Is(err, fs.ErrNotExist) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan")) {
			t.Skipf("no hardware render device: %v", err)
		}
		t.Fatalf("app exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("app did not start")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	metas, err := sessiondir.List(runtimeDir)
	if err != nil || len(metas) != 1 {
		t.Fatalf("sessions: %v %v", metas, err)
	}
	statePath := metas[0].State
	input <- ports.SecurityInput{Event: ports.KeyEvent{Keysym: "Return", Mods: domain.ModSuper, Pressed: true}}
	deadline := time.After(10 * time.Second)
	// The state file is written by its own worker after the scene.
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	mapped := false
	for {
		if mapped {
			st, err := statefile.Read(statePath)
			if err == nil && st.Output == "tv" && len(st.Windows) == 1 && st.Windows[0].Output == "tv" {
				return
			}
		}
		select {
		case <-tick.C:
		case set := <-scenes:
			if len(set) == 1 && set[0].Output == "" {
				continue // before the first output
			}
			if len(set) != 1 || set[0].Output != "tv" || set[0].OutputWidth != 640 {
				t.Fatalf("scenes: want only tv 640 wide, got %+v", set)
			}
			mapped = mapped || len(set[0].Windows) == 1
		case err := <-done:
			t.Fatalf("app exited early: %v", err)
		case <-deadline:
			st, err := statefile.Read(statePath)
			t.Fatalf("no window mapped on a focused tv: %+v %v", st, err)
		}
	}
}
