package core_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// repeatRig runs a Core with five windows in a row on one output, the last
// one focused, and returns its input, config and command channels.
func repeatRig(t *testing.T) (chan ports.InputEvent, chan ports.ConfigChanged, chan ports.ClientCommand, ports.Config) {
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
	return input, reload, commands, cfg
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

// Through Run: a held cmd+Left repeats on its timer, each repeat reports
// user activity (idle timers must not fire while a key is held), and a
// config reload ends it, since the new keymap may swallow the release.
func TestHeldKeyRepeatsThroughRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, reload, commands, cfg := repeatRig(t)
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

		reload <- ports.ConfigChanged{Config: cfg}
		synctest.Wait()
		drain(commands)
		time.Sleep(time.Second)
		synctest.Wait()
		if focus, _ := drain(commands); focus != 0 {
			t.Fatalf("after the reload: %d focus changes, want none", focus)
		}
	})
}
