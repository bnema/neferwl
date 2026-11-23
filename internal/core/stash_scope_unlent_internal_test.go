package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// placedCount reports how many times id is laid out on m and listed in the
// state.
func placedCount(c *Core, m *Monitor, id WindowID) (layout, state int) {
	for _, p := range m.Layout() {
		if p.ID == id {
			layout++
		}
	}
	for _, v := range c.state().Windows {
		if v.ID == id {
			state++
		}
	}
	return layout, state
}

// stashCount is how many workspaces, on every screen, see id stashed.
func stashCount(c *Core, id WindowID) int {
	n := 0
	for _, sc := range c.screens {
		for w := range sc.mon.all() {
			if w.StashIndex(id) >= 0 {
				n++
			}
		}
	}
	return n
}

// A lendee released while its monitor previews leaves no stash window in
// no workspace: an unmapped one goes, the others stay placed once.
func TestOutputStashReleasedWhilePreviewing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, false)
		stashTile(a.Current(), 9)
		c.removeScreen("B")
		c.lendStashes()
		a.ToggleOverview()
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200}) // replug releases A's lendee
		c.lendStashes()
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		a.ToggleOverview()
		c.lendStashes()
		if stashCount(c, 2) != 0 {
			t.Fatal("unmapped window still stashed")
		}
		if l, s := placedCount(c, a, 9); l != 1 || s != 1 {
			t.Fatalf("window 9 laid out %d, listed %d", l, s)
		}
	})
}

func TestGlobalStashReleasedWhilePreviewing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		stashTile(a.Current(), 9)
		c.lendStashes()
		a.ToggleOverview()
		lendee := a.Current()
		a.take(lendee) // its workspace leaves the holder screen
		b.adopt(lendee, false, len(b.Workspaces))
		c.lendStashes()
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		a.ToggleOverview()
		c.lendStashes()
		if stashCount(c, 2) != 0 {
			t.Fatal("unmapped window still stashed")
		}
		if l, s := placedCount(c, a, 9); l != 1 || s != 1 {
			t.Fatalf("window 9 laid out %d, listed %d", l, s)
		}
	})
}
