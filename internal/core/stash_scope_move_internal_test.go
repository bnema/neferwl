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
