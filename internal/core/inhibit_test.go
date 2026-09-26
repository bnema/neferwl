package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/ports"
)

// commandOf waits for the next command of type T, skipping others.
func commandOf[T ports.ClientCommand](t *testing.T, commands <-chan ports.ClientCommand) T {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-commands:
			if v, ok := c.(T); ok {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T", zero)
			return zero
		}
	}
}

// A focused window inhibiting shortcuts gets bound keys; it stops when
// it loses focus, and binds run again.
func TestShortcutsInhibit(t *testing.T) {
	cfg := config.Defaults()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 4)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	spawn := make(chan ports.SpawnRequest, 4)
	state := make(chan ports.State, 1)
	output := make(chan ports.OutputEvent, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Output: output, Commands: commands, Scenes: scenes, Spawn: spawn, State: state})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.ShortcutsInhibit{Window: 1, Active: true}
	if v := commandOf[ports.ShortcutsInhibitState](t, commands); v != (ports.ShortcutsInhibitState{Window: 1, Active: true}) {
		t.Fatal(v)
	}
	bound := ports.KeyEvent{Keysym: "Return", Mods: ports.ModSuper, Pressed: true}
	input <- bound
	if v := commandOf[ports.ForwardKey](t, commands); v != (ports.ForwardKey{ID: 1, Key: bound}) {
		t.Fatal(v)
	}
	select {
	case r := <-spawn:
		t.Fatalf("bind ran: %v", r)
	case <-time.After(30 * time.Millisecond):
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
	deadline := time.After(2 * time.Second)
	for {
		select {
		case st := <-state:
			for _, w := range st.Windows {
				if w.ID == 2 && w.IdleInhibit {
					return
				}
			}
		case <-deadline:
			t.Fatal("idle inhibit not in state")
		}
	}
}
