package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// Moving a stashed window to another workspace of its output does nothing
// in output and global scope: the stash is shared there.
func TestSharedStashMoveWindowToWorkspaceNoop(t *testing.T) {
	for _, scope := range []domain.StashScope{domain.StashScopeOutput, domain.StashScopeGlobal} {
		t.Run(scope.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, scope, "A")
				m := c.cur().mon
				m.AddWindow(1)
				stashTile(m.Current(), 2)
				stashTile(m.Current(), 3)
				c.lendStashes()
				w := m.Current()
				w.FocusID(2)
				c.dispatchAction("move-window-to-workspace 2")
				c.lendStashes()
				if m.Current() != w || !slices.Equal(seenStash(w), []WindowID{2, 3}) || w.StashID(w.StashAt()) != 2 {
					t.Fatalf("stash %v at %d", seenStash(w), w.StashAt())
				}
				if id, _ := w.Focused(); id != 2 {
					t.Fatalf("focused %d", id)
				}
			})
		})
	}
}

// In the overview, a column preview selected after the stash card moves
// to a workspace even though the stash keeps the workspace's focus.
func TestSharedStashOverviewColumnMoves(t *testing.T) {
	for _, scope := range []domain.StashScope{domain.StashScopeOutput, domain.StashScopeGlobal} {
		t.Run(scope.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, scope, "A")
				m := c.cur().mon
				m.AddWindow(1)
				stashTile(m.Current(), 2)
				c.lendStashes()
				w := m.Current()
				m.ToggleOverview()
				m.clearCard() // the selection leaves the stash card for the column
				if id, _ := w.Focused(); id != 2 || m.overviewTarget() != 1 {
					t.Fatalf("setup: focused %d target %d", id, m.overviewTarget())
				}
				c.dispatchAction("move-window-to-workspace 2")
				if w.Has(1) || !m.Workspaces[1].Has(1) {
					t.Fatal("tile 1 did not move")
				}
			})
		})
	}
}
