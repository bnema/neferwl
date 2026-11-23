package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
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
