package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

// A client fullscreen request of a hidden stashed window leaves it hidden
// on its workspace, even in fixed overflow (which moves fullscreen windows
// out).
func TestHiddenFloatFullscreenRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitor("", "")
		m.SetOutput(100, 80)
		m.SetOverflow(OverflowFixed)
		m.AddWindow(1)
		m.AddWindow(2)
		w := m.Current()
		w.ToggleWindowStash()
		w.ToggleStashVisible()
		m.SetFullscreen(2, true)
		if m.Current() != w || w.StashIndex(2) < 0 || !w.Inspect().StashHidden || w.Fullscreen != 0 {
			t.Fatalf("hidden stash left or showed: %+v", w)
		}
		w.ToggleStashVisible()
		if w.Fullscreen != 2 {
			t.Fatalf("fullscreen lost on show: %d", w.Fullscreen)
		}

	})
}

func TestFloatingSceneDimAndConfigure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := ports.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		cfg.Floating.Dim = 0.3
		cfg.Border.Width = 2
		commands := make(chan ports.ClientCommand, 128)
		scenes := make(chan []ports.Scene, 1)
		c, err := New(cfg, Channels{Commands: commands, Scenes: scenes}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		w := c.cur().mon.Current()
		c.cur().mon.SetOutput(100, 80)
		w.AddWindow(1)
		ctx := context.Background()
		check := func(want float64) {
			t.Helper()
			if err := c.publish(ctx); err != nil {
				t.Fatal(err)
			}
			if s := (<-scenes)[0]; s.Dim != want {
				t.Fatalf("dim %v want %v", s.Dim, want)
			}
		}
		check(0)
		w.ToggleWindowStash()
		check(0.3)
		found := false
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 1 {
				if v.Floating {
					found = true
					if v.Width != 76 || v.Height != 60 {
						t.Fatalf("imposed configure %+v", v)
					}
				}
			}
		}
		if !found {
			t.Fatal("missing imposed configure")
		}
		w.AddFloating(2, 20, 10)
		check(0.3)
		found = false
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 2 {
				found = true
				if v.Width != 0 || v.Height != 0 {
					t.Fatalf("native configure %+v", v)
				}
			}
		}
		if !found {
			t.Fatal("missing native configure")
		}
		// Hiding the stash leaves the dialog, and its veil.
		w.ToggleStashVisible()
		check(0.3)
		w.RemoveWindow(2)
		check(0)
		w.ToggleStashVisible()
		check(0.3)
		w.AddFloating(2, 20, 10)
		w.SetFullscreen(1, true)
		check(0)
		w.SetFullscreen(1, false)
		w.ToggleWindowStash()
		w.SetFullscreen(1, true)
		check(0)
		// A dialog of a fullscreen tile waits hidden: no veil over the game.
		w.SetFullscreen(1, false)
		w.RemoveWindow(1)
		w.RemoveWindow(2)
		w.AddWindow(3)
		w.SetFullscreen(3, true)
		check(0)
		w.AddFloating(4, 20, 10)
		check(0)
		w.SetFullscreen(3, false)
		check(0.3)

	})
}
