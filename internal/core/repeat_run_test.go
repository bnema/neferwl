package core_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// rig is a running Core's channels and config.
type rig struct {
	input    chan ports.InputEvent
	client   chan ports.ClientEvent
	reload   chan ports.ConfigChanged
	commands chan ports.ClientCommand
	cfg      ports.Config
}

// repeatRig runs a Core with five windows in a row on one output, the last
// one focused.
func repeatRig(t *testing.T) rig {
	t.Helper()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 8)
	output := make(chan ports.OutputEvent, 1)
	reload := make(chan ports.ConfigChanged, 1)
	commands := make(chan ports.ClientCommand, 256)
	cfg := scrollDefaults()
	cfg.Layout.MaxColumns = 5
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Output: output, Config: reload, ConfigErrors: make(chan error, 1), Commands: commands, Scenes: make(chan []ports.Scene, 1), Spawn: make(chan ports.SpawnRequest, 4)}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 500, Height: 100}}
	for id := ports.WindowID(1); id <= 5; id++ {
		client <- ports.WindowMapped{ID: id}
	}
	synctest.Wait()
	return rig{input, client, reload, commands, cfg}
}

// drain counts the focus changes and activity reports sent since the last
// drain.
func drain(commands chan ports.ClientCommand) (focus, activity int) {
	for len(commands) > 0 {
		switch (<-commands).(type) {
		case ports.FocusWindow:
			focus++
		case ports.UserActivity:
			activity++
		}
	}
	return focus, activity
}

var cmdLeft = ports.KeyEvent{Keysym: "Left", Keycode: 113, Mods: ports.ModSuper, Pressed: true}

// Through Run: a held cmd+Left repeats on its timer and each repeat reports
// user activity, so idle timers do not fire while a key is held.
func TestHeldKeyRepeatsThroughRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := repeatRig(t)
		input, commands := r.input, r.commands
		drain(commands)
		input <- cmdLeft
		synctest.Wait()
		if focus, _ := drain(commands); focus != 1 {
			t.Fatalf("press: %d focus changes, want 1", focus)
		}
		// Past the first repeat and the activity throttle.
		time.Sleep(time.Second)
		synctest.Wait()
		if focus, activity := drain(commands); focus < 2 || activity == 0 {
			t.Fatalf("held: %d focus changes, %d activity reports", focus, activity)
		}
	})
}

// A held key stops repeating when what it ran no longer applies. Each
// event lands before the first repeat, while windows remain to the left,
// so a surviving repeat would move focus or, behind a launcher holding the
// keyboard, still report activity.
func TestRepeatStopsThroughRun(t *testing.T) {
	for _, tt := range []struct {
		name string
		send func(rig)
	}{
		// The new keymap may swallow the release.
		{"config reload", func(r rig) { r.reload <- ports.ConfigChanged{Config: r.cfg} }},
		{"launcher takes the keyboard", func(r rig) {
			r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{{ID: 9, Layer: ports.LayerOverlay, Width: 100, Height: 40, Keyboard: 1}}}
		}},
		{"window inhibits shortcuts", func(r rig) { r.client <- ports.ShortcutsInhibit{Window: 4, Active: true} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := repeatRig(t)
				drain(r.commands)
				r.input <- cmdLeft
				synctest.Wait()
				if focus, _ := drain(r.commands); focus != 1 {
					t.Fatalf("press: %d focus changes, want 1", focus)
				}
				tt.send(r)
				synctest.Wait()
				drain(r.commands)
				time.Sleep(time.Second)
				synctest.Wait()
				if focus, activity := drain(r.commands); focus != 0 || activity != 0 {
					t.Fatalf("%d focus changes, %d activity reports, want none", focus, activity)
				}
			})
		})
	}
}
