package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// publishDrain runs a publish and drops the scene it sends.
func publishDrain(t *testing.T, c *Core) {
	t.Helper()
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.ch.Scenes:
	default:
	}
}

// A holder screen unplugged and plugged in again before any lend leaves
// the session stash on the focused screen.
func TestGlobalHolderUnplugReplugNoLend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := globalRig(t)
		c.focusScreen = 1
		c.applyAction(ActionToggleStashVisible) // holder B
		if c.stashHolder != "B" {
			t.Fatalf("setup: holder %q", c.stashHolder)
		}
		c.focusScreen = 0
		c.removeScreen("B")
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		c.lendStashes()
		if c.stashHolder != "A" || !slices.Equal(seenStash(c.screens[0].mon.Current()), []WindowID{2}) {
			t.Fatalf("holder %q a %v", c.stashHolder, seenStash(c.screens[0].mon.Current()))
		}
	})
}

// A tile dropped on another screen's stash edge brings the session stash
// there.
func TestGlobalStashDropMovesHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, b := globalRig(t)
		a.AddWindow(6)
		c.drop(6, dropTarget{a: Anchor{Kind: AnchorEdge, Action: domain.DropStash}, screen: c.screens[1], ws: b.Current()})
		c.lendStashes()
		if c.stashHolder != "B" || a.Current().StashLen() != 0 || !slices.Contains(seenStash(b.Current()), 6) || !slices.Contains(seenStash(b.Current()), 2) {
			t.Fatalf("holder %q a %v b %v", c.stashHolder, seenStash(a.Current()), seenStash(b.Current()))
		}
	})
}

// A targeted move-window-to-workspace of a shared stash window does
// nothing.
func TestSharedStashTargetedMoveNoop(t *testing.T) {
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
				if err := c.runTargeted(context.Background(), 2, "move-window-to-workspace 2"); err != nil {
					t.Fatal(err)
				}
				c.lendStashes()
				if m.Current() != w || !slices.Equal(seenStash(w), []WindowID{2, 3}) || w.StashID(w.StashAt()) != 3 {
					t.Fatalf("stash %v at %d", seenStash(w), w.StashAt())
				}
			})
		})
	}
}

// Reload from global to workspace scope gives the session stash to the
// holder's current workspace, which keeps it.
func TestStashScopeReloadGlobalToWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, _ := globalRig(t)
		ws1 := a.Current()
		c.cfg.Stash.Scope = domain.StashScopeWorkspace
		c.lendStashes()
		if !slices.Equal(seenStash(ws1), []WindowID{2}) || c.stash.Len() != 0 || c.stashLendee != nil || ws1.StashLent() {
			t.Fatalf("ws1 %v lent %v session %d", seenStash(ws1), ws1.StashLent(), c.stash.Len())
		}
		a.Focus(1)
		c.lendStashes()
		if !slices.Equal(seenStash(ws1), []WindowID{2}) || a.Current().StashLen() != 0 {
			t.Fatal("workspace scope moved the stash")
		}
	})
}

// A lent stash shown over a covering tile keeps showing over it when a
// background window joins it.
func TestOutputStashOverCoverAbsorbs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A")
		m := c.cur().mon
		ws1 := m.Current()
		m.AddWindow(1)
		stashTile(ws1, 2)
		c.lendStashes()
		m.Focus(1)
		m.AddWindow(3)
		c.lendStashes()
		ws2 := m.Current()
		ws2.FocusID(3)
		ws2.SetFullscreen(3, true)
		if !ws2.StashShown() {
			ws2.ToggleStashVisible()
		}
		if ws2.Inspect().StashOver != 3 || !ws2.StashShown() {
			t.Fatalf("setup: over %d shown %v", ws2.Inspect().StashOver, ws2.StashShown())
		}
		ws1.StashSpawned(9)
		c.lendStashes()
		if ws2.Inspect().StashOver != 3 || !ws2.StashShown() || ws2.Fullscreen != 3 {
			t.Fatalf("over %d shown %v fullscreen %d", ws2.Inspect().StashOver, ws2.StashShown(), ws2.Fullscreen)
		}
		for _, p := range ws2.Layout() {
			if p.ID == 9 && p.Hidden {
				t.Fatal("9 hidden")
			}
		}
		if !slices.Contains(seenStash(ws2), 9) {
			t.Fatalf("stash %v", seenStash(ws2))
		}
	})
}

// An overview that closes on another workspace hands the shared stash to
// it; no card is left on the old one.
func TestSharedStashOverviewSwitch(t *testing.T) {
	for _, scope := range []domain.StashScope{domain.StashScopeOutput, domain.StashScopeGlobal} {
		t.Run(scope.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, scope, "A")
				m := c.cur().mon
				ws1 := m.Current()
				m.AddWindow(1)
				stashTile(ws1, 2)
				m.Focus(1)
				m.AddWindow(3)
				ws2 := m.Current()
				m.Focus(0)
				c.lendStashes()
				m.ToggleOverview()
				m.Apply("focus-workspace 2")
				c.lendStashes()
				if m.Current() != ws2 {
					t.Fatal("setup: overview did not select workspace 2")
				}
				m.ToggleOverview()
				c.lendStashes()
				if ws1.StashLen() != 0 || !slices.Equal(seenStash(ws2), []WindowID{2}) {
					t.Fatalf("ws1 %v ws2 %v", seenStash(ws1), seenStash(ws2))
				}
				if m.ov.cardOf != nil && m.ov.cardOf != ws2 {
					t.Fatal("a card refers to a stale workspace")
				}
			})
		})
	}
}

// A stash-only change is a new state, and a cloned state keeps its stash
// when the next state is built.
func TestStateStashChangePublishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A")
		m := c.cur().mon
		m.AddWindow(1)
		stashTile(m.Current(), 2)
		stashTile(m.Current(), 3)
		c.lendStashes()
		before := cloneState(c.state())
		m.Current().ToggleStashVisible()
		if sameState(before, c.state()) {
			t.Fatal("hiding the stash is not a state change")
		}
		kept := cloneState(c.state())
		w := m.Current()
		w.FocusID(2)
		w.ToggleWindowStash() // 2 leaves the stash
		_ = c.state()
		for _, ws := range kept.Workspaces {
			if len(ws.Stash) > 0 && !slices.Equal(ws.Stash, []WindowID{2, 3}) {
				t.Fatalf("cloned stash changed: %v", ws.Stash)
			}
		}
	})
}

// Real entry points only: a publish lends the shared stash after a
// workspace switch (output) and after its holder is unplugged (global).
func TestSharedStashThroughEntryPoints(t *testing.T) {
	t.Run("output", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := scopeCore(t, domain.StashScopeOutput, "A")
			m := c.cur().mon
			m.AddWindow(1)
			m.AddWindow(2)
			c.applyAction(ActionToggleWindowStash) // 2, the focused tile
			publishDrain(t, c)
			c.applyAction(ActionFocusWorkspaceNext)
			publishDrain(t, c)
			ws2 := m.Current()
			if m.Active != 1 || !slices.Equal(seenStash(ws2), []WindowID{2}) || !ws2.StashShown() {
				t.Fatalf("active %d stash %v", m.Active, seenStash(ws2))
			}
		})
	})
	t.Run("global", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := scopeCore(t, domain.StashScopeGlobal, "A", "B")
			a, b := c.screens[0].mon, c.screens[1].mon
			a.AddWindow(1)
			b.AddWindow(3)
			b.AddWindow(4)
			c.focusScreen = 1
			c.applyAction(ActionToggleWindowStash) // 4 on B, the holder
			publishDrain(t, c)
			if c.stashHolder != "B" || !slices.Equal(seenStash(b.Current()), []WindowID{4}) {
				t.Fatalf("setup: holder %q b %v", c.stashHolder, seenStash(b.Current()))
			}
			c.focusScreen = 0
			c.removeScreen("B")
			publishDrain(t, c)
			if got := seenStash(a.Current()); !slices.Equal(got, []WindowID{4}) || c.stashHolder != "A" {
				t.Fatalf("holder %q a %v", c.stashHolder, got)
			}
		})
	})
}

// A reload through apply merges the stashes into the output stash, and
// move-window-to-workspace then leaves a stashed window where it is.
func TestStashScopeReloadThroughApply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeWorkspace, "A")
		m := c.cur().mon
		ws1 := m.Current()
		m.AddWindow(1)
		stashTile(ws1, 2)
		m.Focus(1)
		m.AddWindow(3)
		stashTile(m.Current(), 4)
		ws2 := m.Current()
		publishDrain(t, c)
		cfg := c.cfg
		cfg.Stash.Scope = domain.StashScopeOutput
		if err := c.apply(cfg); err != nil {
			t.Fatal(err)
		}
		publishDrain(t, c)
		if got := seenStash(ws2); !slices.Equal(got, []WindowID{2, 4}) || ws2.StashID(ws2.StashAt()) != 4 || ws1.StashLen() != 0 {
			t.Fatalf("ws2 %v at %d, ws1 %v", got, ws2.StashAt(), seenStash(ws1))
		}
		c.applyAction("move-window-to-workspace 1")
		publishDrain(t, c)
		if m.Current() != ws2 || !slices.Equal(seenStash(ws2), []WindowID{2, 4}) || ws2.StashID(ws2.StashAt()) != 4 {
			t.Fatalf("after move: ws2 %v at %d", seenStash(ws2), ws2.StashAt())
		}
	})
}
