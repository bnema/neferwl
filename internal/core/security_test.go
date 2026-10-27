package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestSecurityOwnerAdversarialQueues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var epoch atomic.Value
		epoch.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) })
		client := make(chan ports.ClientEvent, 32)
		input := make(chan ports.InputEvent, 32)
		output := make(chan ports.OutputEvent, 8)
		reload := make(chan domain.ConfigChanged, 8)
		commands := make(chan ports.ClientCommand, 256)
		scenes := make(chan []domain.Scene, 1)
		spawn := make(chan domain.SpawnRequest, 8)
		constraints := make(chan domain.PointerConstraint, 1)
		cfg := scrollDefaults()
		cfg.Terminal.Command = []string{"terminal"}
		cfg.Terminal.AutoOpen = "off"
		cfg.Binds = map[string]string{"Super+q": "quit"}
		c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Scenes: scenes, Spawn: spawn, Constraints: constraints}, testOptions(t, core.Options{Security: gate, Terminal: true}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			if err := receive(t, done); err != nil {
				t.Error(err)
			}
		})
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 300, Height: 200}}
		scene(t, scenes)
		client <- ports.WindowMapped{ID: 1}
		scene(t, scenes)
		// A pre-lock key is queued BEFORE any SessionLockChanged event. The gate
		// must block it regardless of which channel select chooses next.
		locked := domain.SecurityState{Generation: 1, Protected: true}
		epoch.Store(locked)
		input <- ports.SecurityInput{State: domain.SecurityState{}, Event: ports.KeyEvent{Keysym: "q", Mods: domain.ModSuper, Pressed: true}}
		s := sceneMatch(t, scenes, func(s domain.Scene) bool { return s.Security == locked })
		assertProtected(t, s)
		if len(s.Windows) != 0 {
			t.Fatal(s)
		}
		receive(t, constraints)
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "B", Width: 200, Height: 100}}
		sceneMatch(t, scenes, func(s domain.Scene) bool { return s.Security == locked })
		surfaces := []domain.LockSurfacePlacement{{ID: 10, Output: "A", Width: 300, Height: 200}, {ID: 20, Output: "B", Width: 200, Height: 100}}
		client <- ports.SessionLockChanged{State: locked, Surfaces: surfaces}
		s = sceneMatch(t, scenes, func(s domain.Scene) bool { return len(s.Windows) == 1 && s.Windows[0].ID == 10 })
		assertProtected(t, s)
		// Raw input is refused even in the initial unlocked epoch when wired;
		// stale, raw, bind and swipe events must not activate desktop behavior.
		input <- ports.KeyEvent{Keysym: "raw", Pressed: true}
		input <- ports.SecurityInput{State: locked, Event: ports.KeyEvent{Keysym: "q", Mods: domain.ModSuper, Pressed: true}}
		input <- ports.SecurityInput{State: locked, Event: ports.SwipeBegin{Fingers: 3}}
		input <- ports.SecurityInput{State: locked, Event: ports.KeyEvent{Keysym: "password", Keycode: 30, Pressed: true}}
		expectKey(t, commands, locked, 10, "password")
		input <- ports.SecurityInput{State: locked, Event: ports.PointerMotion{X: 350, Y: 30}}
		input <- ports.SecurityInput{State: locked, Event: ports.PointerButton{Button: 0x110, Pressed: true}}
		input <- ports.SecurityInput{State: locked, Event: ports.KeyEvent{Keysym: "clicked", Pressed: true}}
		expectKey(t, commands, locked, 20, "clicked")
		// Stale surfaces cannot replace the current lock roles. Invalid logical
		// dimensions and unknown outputs never become drawn/input targets.
		client <- ports.SessionLockChanged{State: domain.SecurityState{}, Surfaces: []domain.LockSurfacePlacement{{ID: 1, Output: "A", Width: 300, Height: 200}}}
		client <- ports.SessionLockChanged{State: locked, Surfaces: append(surfaces, domain.LockSurfacePlacement{ID: 99, Output: "unknown", Width: 300, Height: 200})}
		s = sceneMatch(t, scenes, func(s domain.Scene) bool { return len(s.Windows) == 1 && s.Windows[0].ID == 10 })
		assertProtected(t, s)
		cfg.Terminal.AutoOpen = "always"
		cfg.Startup = [][]string{{"must-not-start"}}
		reload <- domain.ConfigChanged{Config: cfg}
		s = scene(t, scenes)
		assertProtected(t, s)
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "C", Width: 100, Height: 100}}
		for _, s := range receive(t, scenes) {
			assertProtected(t, s)
			if s.Output == "C" && len(s.Windows) != 0 {
				t.Fatal(s)
			}
		}
		select {
		case req := <-spawn:
			t.Fatalf("protected spawn: %+v", req)
		default:
		}
		// Unlock discards all presses/grabs, including a password press with no
		// corresponding release until AFTER the transition.
		unlocked := domain.SecurityState{Generation: 2}
		epoch.Store(unlocked)
		input <- ports.SecurityInput{State: locked, Event: ports.KeyEvent{Keysym: "stale-password", Pressed: true}}
		input <- ports.SecurityInput{State: unlocked, Event: ports.KeyEvent{Keysym: "password", Keycode: 30, Pressed: false}}
		input <- ports.SecurityInput{State: unlocked, Event: ports.KeyEvent{Keysym: "desktop", Pressed: true}}
		expectKey(t, commands, unlocked, 1, "desktop")
		s = sceneMatch(t, scenes, func(s domain.Scene) bool { return s.Security == unlocked })
		if len(s.Windows) == 0 || s.Windows[0].ID != 1 {
			t.Fatal(s)
		}

	})
}

func assertProtected(t *testing.T, s domain.Scene) {
	t.Helper()
	if !s.Security.Protected || s.Background != "#000000" || s.Capture != nil || s.CaptureScene != nil || len(s.Layers) != 0 || len(s.Separators) != 0 || s.Border.Width != 0 || s.Dim != 0 || s.WorkspaceClip != (domain.Rect{}) {
		t.Fatalf("unsafe scene: %+v", s)
	}
	for _, w := range s.Windows {
		if !w.Fullscreen || w.Hidden || w.Popup || w.Rect != (domain.Rect{W: s.OutputWidth, H: s.OutputHeight}) {
			t.Fatalf("unsafe role: %+v", w)
		}
	}
}

func expectKey(t *testing.T, ch <-chan ports.ClientCommand, state domain.SecurityState, id domain.WindowID, name string) {
	t.Helper()
	for {
		wrapped, ok := receive(t, ch).(ports.SecurityCommand)
		if !ok {
			t.Fatal("unwrapped command")
		}
		if k, ok := wrapped.Command.(ports.ForwardKey); ok {
			if wrapped.State != state {
				continue
			} // older commands keep their epoch
			if k.Key.Keysym == "raw" || k.Key.Keysym == "stale-password" || !k.Key.Pressed {
				t.Fatalf("leaked input: %+v", k)
			}
			if k.Key.Keysym == name {
				if k.ID != id {
					t.Fatalf("key target %d want %d", k.ID, id)
				}
				return
			}
		}
	}
}
