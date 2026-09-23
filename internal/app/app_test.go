package app

import (
	"context"
	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/ports"
	"os/exec"
	"testing"
	"time"
)

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
					if count == 1 && s.Windows[0].Rect.W != 948 {
						t.Errorf("width = %d, want 948", s.Windows[0].Rect.W)
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
