package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// outputRig has output-scope screens A (focused, stash [2]) and B (stash
// [4, 5]), each with one tile.
func outputRig(t *testing.T, virtualB bool) (*Core, *Monitor) {
	t.Helper()
	c := scopeCore(t, domain.StashScopeOutput, "A")
	c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200, Virtual: virtualB})
	a, b := c.screens[0].mon, c.screens[1].mon
	a.AddWindow(1)
	stashTile(a.Current(), 2)
	b.AddWindow(3)
	stashTile(b.Current(), 4)
	stashTile(b.Current(), 5)
	c.focusScreen = 0
	c.lendStashes()
	return c, a
}

func replugB(c *Core) *Monitor {
	c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
	c.lendStashes()
	return c.screens[c.screenIndex("B")].mon
}

func TestOutputStashReturnsOnReplug(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, false)
		c.removeScreen("B")
		c.lendStashes()
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2, 4, 5}) {
			t.Fatalf("unplugged: a %v", got)
		}
		b := replugB(c)
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2}) {
			t.Fatalf("replugged: a %v", got)
		}
		if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{4, 5}) {
			t.Fatalf("replugged: b %v", got)
		}
		if b.Current().StashShown() {
			t.Fatal("returned stash shown, want hidden")
		}
		if w := a.Current(); w.StashID(w.StashAt()) != 2 {
			t.Fatalf("a selection %d", w.StashID(w.StashAt()))
		}
	})
}

func TestOutputStashUnstashedStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, false)
		c.removeScreen("B")
		c.lendStashes()
		w := a.Current()
		w.FocusID(4)
		w.ToggleWindowStash() // 4 tiles on A
		w.FocusID(4)
		w.ToggleWindowStash() // and back into A's stash: no home
		c.lendStashes()
		b := replugB(c)
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2, 4}) {
			t.Fatalf("a %v", got)
		}
		if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{5}) {
			t.Fatalf("b %v", got)
		}
	})
}

func TestVirtualOutputStashStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRig(t, true)
		c.removeScreen("B")
		c.lendStashes()
		b := replugB(c)
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2, 4, 5}) || b.Current().StashLen() != 0 {
			t.Fatalf("a %v b %v", got, seenStash(b.Current()))
		}
	})
}

// Workspace to global merges in screen, workspace, arrival order, with the
// focused screen's current selection.
func TestStashScopeReloadWorkspaceGlobalOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeWorkspace, "A", "B")
		a, b := c.screens[0].mon, c.screens[1].mon
		a.AddWindow(1)
		stashTile(a.Current(), 2)
		b.AddWindow(3)
		stashTile(b.Current(), 4)
		stashTile(b.Current(), 5)
		b.Current().FocusID(4)
		b.Focus(1)
		b.AddWindow(6)
		stashTile(b.Current(), 7)
		b.Focus(0)
		c.focusScreen = 1
		c.lendStashes()
		c.cfg.Stash.Scope = domain.StashScopeGlobal
		c.lendStashes()
		w := b.Current()
		if got := seenStash(w); !slices.Equal(got, []WindowID{2, 4, 5, 7}) || w.StashID(w.StashAt()) != 4 {
			t.Fatalf("stash %v at %d", got, w.StashAt())
		}
	})
}

// outputRigIdentified is outputRig with B identified by EDID bb01.
func outputRigIdentified(t *testing.T) (*Core, *Monitor) {
	t.Helper()
	c, a := outputRig(t, false)
	c.screens[1].info.EDIDID, c.screens[1].info.EDIDHash = "bb01", "h1"
	return c, a
}

func plugAs(c *Core, info domain.OutputInfo) *Monitor {
	info.Width, info.Height = 300, 200
	c.addScreen(info)
	c.lendStashes()
	return c.screens[c.screenIndex(info.Name)].mon
}

// B replugged on another connector is recognized by its EDID identifier: its
// stash windows return there, hidden (AC7).
func TestOutputStashReturnsOnAnotherConnector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRigIdentified(t)
		c.removeScreen("B")
		c.lendStashes()
		b := plugAs(c, domain.OutputInfo{Name: "C", EDIDID: "bb01", EDIDHash: "h1"})
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2}) {
			t.Fatalf("a %v", got)
		}
		if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{4, 5}) || b.Current().StashShown() {
			t.Fatalf("c %v shown %t", got, b.Current().StashShown())
		}
	})
}

// Another known monitor on B's connector takes nothing.
func TestOutputStashStaysForAnotherMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRigIdentified(t)
		c.removeScreen("B")
		c.lendStashes()
		b := plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "cc01"})
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2, 4, 5}) || b.Current().StashLen() != 0 {
			t.Fatalf("a %v b %v", got, seenStash(b.Current()))
		}
	})
}

// A monitor that shows up on a connector already held by another identity
// (same-connector path of addScreen) gets its stash back.
func TestOutputStashReturnsWhenConnectorIdentityChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRigIdentified(t)
		c.removeScreen("B")
		c.lendStashes()
		plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "cc01"})
		b := plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "bb01", EDIDHash: "h1"})
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2}) {
			t.Fatalf("a %v", got)
		}
		if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{4, 5}) {
			t.Fatalf("b %v", got)
		}
	})
}
