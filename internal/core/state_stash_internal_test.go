package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// In output scope the state lists the shared stash once, on the workspace
// it is lent to, and its windows report that workspace.
func TestStateSharedStash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A")
		m := c.cur().mon
		ws1 := m.Current()
		m.AddWindow(1)
		stashTile(ws1, 2)
		c.lendStashes()
		m.Focus(1)
		ws2 := m.Current()
		c.lendStashes()
		st := c.state()
		if st.StashScope != "output" {
			t.Fatalf("scope %q", st.StashScope)
		}
		holders := 0
		for _, w := range st.Workspaces {
			if len(w.Stash) == 0 {
				continue
			}
			holders++
			if w.ID != ws2.ID || !slices.Equal(w.Stash, []WindowID{2}) || !w.StashShown {
				t.Fatalf("stash on workspace %d: %v shown %v", w.ID, w.Stash, w.StashShown)
			}
		}
		if holders != 1 {
			t.Fatalf("%d workspaces list the stash", holders)
		}
		for _, v := range st.Windows {
			if v.ID == 2 && (v.WorkspaceID != ws2.ID || v.StashIndex != 1 || v.Hidden) {
				t.Fatalf("window 2 %+v", v)
			}
		}
	})
}
