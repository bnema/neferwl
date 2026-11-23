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

// A stashed window moved to a workspace on another output (a rule) joins
// that output's stash in output scope.
func TestOutputStashMoveToOtherOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, false)
		b := c.screens[1].mon
		MoveByRule(a.Current(), b.Current(), 2)
		c.lendStashes()
		if a.Current().StashIndex(2) >= 0 || !slices.Contains(seenStash(b.Current()), 2) {
			t.Fatalf("a %v b %v", seenStash(a.Current()), seenStash(b.Current()))
		}
	})
}
