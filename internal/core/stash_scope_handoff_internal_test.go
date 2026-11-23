package core

import (
	"context"
	"regexp"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Dropping the current named workspace on reload merges it without
// duplicating the shared stash it is lent.
func TestSharedStashMergeNoDuplicate(t *testing.T) {
	for _, scope := range []domain.StashScope{domain.StashScopeOutput, domain.StashScopeGlobal} {
		t.Run(scope.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, scope, "A")
				m := c.cur().mon
				m.SetNamed([]NamedWorkspace{{Name: "work"}})
				m.ToggleNamed("work")
				m.AddWindow(1)
				stashTile(m.Current(), 2)
				c.lendStashes()
				m.SetNamed(nil)
				// Seen at once, before the next lend, and again after it.
				for _, pass := range []string{"merged", "lent"} {
					w := m.Current()
					if got := seenStash(w); !slices.Equal(got, []WindowID{2}) || w.StashAt() != 0 || !w.StashShown() {
						t.Fatalf("%s: stash %v at %d shown %v", pass, got, w.StashAt(), w.StashShown())
					}
					c.lendStashes()
				}
				n := 0
				for w := range m.all() {
					n += w.StashLen()
				}
				if n != 1 {
					t.Fatalf("stash seen %d times", n)
				}
			})
		})
	}
}

// A title rule does not move a window of a shared stash: order, visibility
// and selection stay.
func TestSharedStashTitleRuleNoMove(t *testing.T) {
	for _, tc := range []struct {
		name           string
		scope          domain.StashScope
		workspace, out string
	}{
		{"output to workspace 2", domain.StashScopeOutput, "2", ""},
		{"global to workspace 2", domain.StashScopeGlobal, "2", ""},
		{"global to output B", domain.StashScopeGlobal, "", "B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cfg domain.Config
				cfg.Keyboard.CmdKey = "alt"
				cfg.Layout.MaxColumns = 2
				cfg.Stash.Scope = tc.scope
				cfg.Rules = []domain.WindowRule{{Name: "big", AppID: regexp.MustCompile("^game$"), Title: regexp.MustCompile("big"), Workspace: tc.workspace, Output: tc.out}}
				c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
				if err != nil {
					t.Fatal(err)
				}
				c.addScreen(domain.OutputInfo{Name: "A", Width: 300, Height: 200})
				c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
				c.focusScreen = 0
				m := c.cur().mon
				c.windows.mapped(ports.WindowMapped{ID: 2, AppID: "game", Title: "small"}, c.now())
				m.AddWindow(1)
				stashTile(m.Current(), 2)
				stashTile(m.Current(), 3)
				m.Current().ToggleStashVisible() // hidden
				c.lendStashes()
				w := m.Current()
				if err := c.windowTitle(context.Background(), ports.WindowTitle{ID: 2, Title: "big"}); err != nil {
					t.Fatal(err)
				}
				c.lendStashes()
				if got := seenStash(w); !slices.Equal(got, []WindowID{2, 3}) || w.StashAt() != 1 || w.StashShown() {
					t.Fatalf("stash %v at %d shown %v", got, w.StashAt(), w.StashShown())
				}
			})
		})
	}
}

// A fullscreen stash member of an unplugged output is fullscreen again on
// the host, not left pending in a shown stash.
func TestOutputUnplugKeepsStashFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A", "B")
		a, b := c.screens[0].mon, c.screens[1].mon
		a.AddWindow(1)
		stashTile(a.Current(), 2)
		b.AddWindow(3)
		stashTile(b.Current(), 4)
		c.lendStashes()
		b.Current().SetFullscreen(4, true)
		c.focusScreen = 0
		c.removeScreen("B")
		c.lendStashes()
		w := a.Current()
		if got := seenStash(w); !slices.Equal(got, []WindowID{2, 4}) {
			t.Fatalf("stash %v", got)
		}
		// The stashed cover is the stash on screen (StashShown is false
		// under a cover by definition): the stash itself is not hidden.
		in := w.Inspect()
		if in.StashHidden {
			t.Fatal("stash hidden")
		}
		if w.Fullscreen != 4 || in.HiddenFullscreen != 0 {
			t.Fatalf("fullscreen %d pending %d", w.Fullscreen, in.HiddenFullscreen)
		}
	})
}

// A background workspace handing a pending fullscreen to the lent stash
// makes it fullscreen on the lendee.
func TestSharedDrainAppliesPendingFullscreen(t *testing.T) {
	for _, scope := range []domain.StashScope{domain.StashScopeOutput, domain.StashScopeGlobal} {
		t.Run(scope.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, scope, "A")
				m := c.cur().mon
				m.AddWindow(1)
				stashTile(m.Current(), 2)
				c.lendStashes()
				ws1 := m.Current()
				m.Focus(1)
				m.AddWindow(3)
				c.lendStashes()
				ws1.StashSpawned(5) // in ws1's own stash, shown
				ws1.SetFullscreen(5, true)
				c.lendStashes()
				w := m.Current()
				if in := w.Inspect(); w.Fullscreen != 5 || in.HiddenFullscreen != 0 {
					t.Fatalf("fullscreen %d pending %d stash %v", w.Fullscreen, in.HiddenFullscreen, seenStash(w))
				}
			})
		})
	}
}

// Moving the lendee workspace to another output leaves the stash on its
// output: no window is lost or seen twice.
func TestOutputStashStaysOnMoveWorkspaceToMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, false)
		b := c.screens[1].mon
		moved := a.Current()
		c.moveWorkspace(dirRight)
		c.lendStashes()
		if b.Current() != moved {
			t.Fatal("setup: workspace did not move")
		}
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2}) {
			t.Fatalf("a %v", got)
		}
		if got := seenStash(moved); !slices.Equal(got, []WindowID{4, 5}) {
			t.Fatalf("moved %v", got)
		}
		for _, id := range []WindowID{2, 4, 5} {
			if n := stashCount(c, id); n != 1 {
				t.Fatalf("window %d seen %d times", id, n)
			}
		}
	})
}
