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
				c.lendStashes()
				if got := seenStash(m.Current()); !slices.Equal(got, []WindowID{2}) {
					t.Fatalf("stash %v", got)
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
		scope          domain.StashScope
		workspace, out string
	}{
		{domain.StashScopeOutput, "2", ""},
		{domain.StashScopeGlobal, "2", ""},
		{domain.StashScopeGlobal, "", "B"},
	} {
		t.Run(tc.scope.String()+tc.workspace+tc.out, func(t *testing.T) {
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
