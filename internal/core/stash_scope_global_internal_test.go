package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// globalRig has screens A (focused, holding a shown stash [2]) and B, each
// with one tile.
func globalRig(t *testing.T) (*Core, *Monitor, *Monitor) {
	t.Helper()
	c := scopeCore(t, domain.StashScopeGlobal, "A", "B")
	a, b := c.screens[0].mon, c.screens[1].mon
	a.AddWindow(1)
	b.AddWindow(3)
	stashTile(a.Current(), 2)
	c.focusScreen = 0
	c.lendStashes()
	if !slices.Equal(seenStash(a.Current()), []WindowID{2}) {
		t.Fatalf("setup: %v", seenStash(a.Current()))
	}
	return c, a, b
}

func TestGlobalStashToggleMovesShown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		c.focusScreen = 1
		c.applyAction(ActionToggleStashVisible)
		c.lendStashes()
		wb := b.Current()
		if a.Current().StashLen() != 0 || !slices.Equal(seenStash(wb), []WindowID{2}) || !wb.StashShown() {
			t.Fatalf("a %v b %v shown %v", seenStash(a.Current()), seenStash(wb), wb.StashShown())
		}
		if id, _ := wb.Focused(); id != 2 {
			t.Fatalf("b focused %d", id)
		}
		c.applyAction(ActionToggleStashVisible)
		if wb.StashShown() {
			t.Fatal("second toggle did not hide")
		}
	})
}

func TestGlobalStashToggleShowsHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		c.applyAction(ActionToggleStashVisible) // hide on A
		if a.Current().StashShown() {
			t.Fatal("setup: still shown")
		}
		c.focusScreen = 1
		c.applyAction(ActionToggleStashVisible)
		if !b.Current().StashShown() || a.Current().StashLen() != 0 {
			t.Fatal("not shown on B")
		}
	})
}

// A window landing in another screen's stash joins the session stash on
// the holder, selected.
func TestGlobalStashIndirectStaysOnHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		b.Current().StashSpawned(9) // as a rule would
		c.lendStashes()
		wa := a.Current()
		if b.Current().StashLen() != 0 || !slices.Equal(seenStash(wa), []WindowID{2, 9}) || wa.StashID(wa.StashAt()) != 9 {
			t.Fatalf("a %v at %d, b %v", seenStash(wa), wa.StashAt(), seenStash(b.Current()))
		}
		if c.stashHolder != "A" {
			t.Fatalf("holder %q", c.stashHolder)
		}
	})
}

func TestGlobalStashSpawnMovesHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		req := c.placement.request([]string{"foot"}, spawnTarget{kind: spawnStash, ws: b.Current(), at: c.now()})
		c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
		c.lendStashes()
		wb := b.Current()
		if c.stashHolder != "B" || a.Current().StashLen() != 0 || !slices.Equal(seenStash(wb), []WindowID{2, 5}) || wb.StashID(wb.StashAt()) != 5 {
			t.Fatalf("holder %q b %v at %d", c.stashHolder, seenStash(wb), wb.StashAt())
		}
	})
}

func TestGlobalToggleWindowStashMovesHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		c.focusScreen = 1
		b.Current().FocusID(3)
		c.applyAction(ActionToggleWindowStash)
		c.lendStashes()
		if c.stashHolder != "B" || a.Current().StashLen() != 0 || !slices.Equal(seenStash(b.Current()), []WindowID{2, 3}) {
			t.Fatalf("holder %q b %v", c.stashHolder, seenStash(b.Current()))
		}
	})
}

func TestStashScopeReloadOutputGlobal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A", "B")
		a, b := c.screens[0].mon, c.screens[1].mon
		a.AddWindow(1)
		stashTile(a.Current(), 2)
		b.AddWindow(3)
		stashTile(b.Current(), 4)
		stashTile(b.Current(), 5)
		b.Current().FocusID(4) // selection on B: 4
		c.focusScreen = 1
		c.lendStashes()
		c.cfg.Stash.Scope = domain.StashScopeGlobal
		c.lendStashes()
		wb := b.Current()
		if got := seenStash(wb); !slices.Equal(got, []WindowID{2, 4, 5}) || wb.StashID(wb.StashAt()) != 4 || a.Current().StashLen() != 0 {
			t.Fatalf("b %v at %d, a %v", got, wb.StashAt(), seenStash(a.Current()))
		}
		c.cfg.Stash.Scope = domain.StashScopeOutput
		c.lendStashes()
		if !slices.Equal(seenStash(b.Current()), []WindowID{2, 4, 5}) || a.Current().StashLen() != 0 || c.stash.Len() != 0 {
			t.Fatalf("back to output: b %v a %v", seenStash(b.Current()), seenStash(a.Current()))
		}
	})
}

func TestGlobalStashHolderUnplugged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := globalRig(t)
		c.focusScreen = 1
		c.applyAction(ActionToggleWindowStash) // holder B, 3 stashed
		c.lendStashes()
		c.focusScreen = 0
		c.removeScreen("B")
		c.lendStashes()
		a := c.screens[0].mon
		if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{2, 3}) || c.stashHolder != "A" {
			t.Fatalf("a %v holder %q", got, c.stashHolder)
		}
	})
}

// A targeted toggle-window-stash moves nothing shown: the window joins the
// session stash on its holder screen.
func TestGlobalTargetedStashKeepsHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		if err := c.runTargeted(context.Background(), 3, ActionToggleWindowStash); err != nil {
			t.Fatal(err)
		}
		c.lendStashes()
		if c.stashHolder != "A" || b.Current().StashLen() != 0 || !slices.Equal(seenStash(a.Current()), []WindowID{2, 3}) {
			t.Fatalf("holder %q a %v b %v", c.stashHolder, seenStash(a.Current()), seenStash(b.Current()))
		}
	})
}
