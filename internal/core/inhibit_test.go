package core_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// commandOf waits for the next command of type T, skipping others.
func commandOf[T ports.ClientCommand](t *testing.T, commands <-chan ports.ClientCommand) T {
	t.Helper()
	for {
		synctest.Wait()
		select {
		case c := <-commands:
			if v, ok := c.(T); ok {
				return v
			}
		default:
			var zero T
			t.Fatalf("no %T", zero)
			return zero
		}
	}
}

// A focused window inhibiting shortcuts gets bound keys; it stops when
// it loses focus, and binds run again.
func TestShortcutsInhibit(t *testing.T) { synctest.Test(t, shortcutsInhibit) }

func shortcutsInhibit(t *testing.T) {
	cfg := scrollDefaults()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 4)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []domain.Scene, 1)
	spawn := make(chan domain.SpawnRequest, 4)
	state := make(chan domain.State, 1)
	output := make(chan ports.OutputEvent, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Output: output, Commands: commands, Scenes: scenes, Spawn: spawn, State: state}, testOptions(t, core.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.ShortcutsInhibit{Window: 1, Active: true}
	if v := commandOf[ports.ShortcutsInhibitState](t, commands); v != (ports.ShortcutsInhibitState{Window: 1, Active: true}) {
		t.Fatal(v)
	}
	bound := ports.KeyEvent{Keysym: "Return", Mods: domain.ModSuper, Pressed: true}
	input <- bound
	if v := commandOf[ports.ForwardKey](t, commands); v != (ports.ForwardKey{ID: 1, Key: bound}) {
		t.Fatal(v)
	}
	synctest.Wait()
	select {
	case r := <-spawn:
		t.Fatalf("bind ran: %v", r)
	default:
	}
	// A new window takes focus: the inhibitor goes inactive, binds run.
	client <- ports.WindowMapped{ID: 2}
	if v := commandOf[ports.ShortcutsInhibitState](t, commands); v != (ports.ShortcutsInhibitState{Window: 1}) {
		t.Fatal(v)
	}
	input <- bound
	receive(t, spawn)
	// Idle inhibitors show in the state.
	client <- ports.IdleInhibit{Window: 2, Active: true}
	waitState(t, state, "idle inhibit", func(w domain.WindowState) bool { return w.ID == 2 && w.IdleInhibit })
	// So do shortcuts inhibition and the pointer constraint.
	client <- ports.ShortcutsInhibit{Window: 2, Active: true}
	waitState(t, state, "shortcuts inhibited", func(w domain.WindowState) bool { return w.ID == 2 && w.ShortcutsInhibited })
	client <- ports.PointerConstrained{ID: 2, PointerConstraint: domain.PointerConstraint{Mode: domain.ConstraintLock}}
	waitState(t, state, "locked", func(w domain.WindowState) bool { return w.ID == 2 && w.PointerConstraint == "locked" })
	client <- ports.PointerConstrained{ID: 2, PointerConstraint: domain.PointerConstraint{Mode: domain.ConstraintConfine}}
	waitState(t, state, "confined", func(w domain.WindowState) bool { return w.ID == 2 && w.PointerConstraint == "confined" })
	client <- ports.PointerConstrained{}
	waitState(t, state, "unconstrained", func(w domain.WindowState) bool { return w.ID == 2 && w.PointerConstraint == "" })
}

// waitState drains published states until one has a window matching ok.
func waitState(t *testing.T, state <-chan domain.State, what string, ok func(domain.WindowState) bool) {
	t.Helper()
	for {
		synctest.Wait()
		select {
		case st := <-state:
			if slices.ContainsFunc(st.Windows, ok) {
				return
			}
		default:
			t.Fatal(what, "not in state")
		}
	}
}

// A key whose press ran a bind before inhibiting began is not released to
// the window: it never saw the press.
func TestShortcutsInhibitDropsBoundRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := scrollDefaults()
		input := make(chan ports.InputEvent, 4)
		client := make(chan ports.ClientEvent, 4)
		commands := make(chan ports.ClientCommand, 64)
		scenes := make(chan []domain.Scene, 1)
		spawn := make(chan domain.SpawnRequest, 4)
		output := make(chan ports.OutputEvent, 1)
		c, err := core.New(cfg, core.Channels{Input: input, Client: client, Output: output, Commands: commands, Scenes: scenes, Spawn: spawn}, testOptions(t, core.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() { cancel(); receive(t, done) }()
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
		scene(t, scenes)
		client <- ports.WindowMapped{ID: 1}
		scene(t, scenes)
		press := ports.KeyEvent{Keysym: "Return", Keycode: 36, Mods: domain.ModSuper, Pressed: true}
		input <- press
		receive(t, spawn)
		client <- ports.ShortcutsInhibit{Window: 1, Active: true}
		commandOf[ports.ShortcutsInhibitState](t, commands)
		release := press
		release.Pressed = false
		input <- release
		other := ports.KeyEvent{Keysym: "a", Keycode: 38, Pressed: true}
		input <- other
		if v := commandOf[ports.ForwardKey](t, commands); v.Key != other {
			t.Fatalf("forwarded %+v before %+v", v.Key, other)
		}

	})
}
