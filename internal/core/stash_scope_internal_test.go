package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func scopeCore(t *testing.T, scope domain.StashScope, outs ...string) *Core {
	t.Helper()
	var cfg domain.Config
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 2
	cfg.Stash.Scope = scope
	c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		c.addScreen(domain.OutputInfo{Name: o, Width: 300, Height: 200})
	}
	return c
}

func seenStash(w *Workspace) []WindowID {
	ids := make([]WindowID, 0, w.StashLen())
	for i := range w.StashLen() {
		ids = append(ids, w.StashID(i))
	}
	return ids
}

// stashTile adds tile id to w and stashes it, shown and selected.
func stashTile(w *Workspace, id WindowID) {
	w.AddWindow(id)
	w.FocusID(id)
	w.ToggleWindowStash()
}

func TestOutputStashFollowsWorkspace(t *testing.T) {
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
		if ws1.StashLen() != 0 || ws1.Has(2) {
			t.Fatalf("ws1 still sees %v", seenStash(ws1))
		}
		if !slices.Equal(seenStash(ws2), []WindowID{2}) || !ws2.StashShown() {
			t.Fatalf("ws2 stash %v shown %v", seenStash(ws2), ws2.StashShown())
		}
		if id, _ := ws2.Focused(); id != 2 {
			t.Fatalf("ws2 focused %d", id)
		}
	})
}

// A stash spawn into the workspace left joins the monitor stash, selected.
func TestOutputStashSpawnIntoBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A")
		m := c.cur().mon
		ws1 := m.Current()
		m.AddWindow(1)
		stashTile(ws1, 2)
		c.lendStashes()
		m.Focus(1)
		c.lendStashes()
		req := c.placement.request([]string{"firefox"}, spawnTarget{kind: spawnStash, ws: ws1, at: c.now()})
		c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
		c.lendStashes()
		ws2 := m.Current()
		if got := seenStash(ws2); !slices.Equal(got, []WindowID{2, 5}) || ws2.StashID(ws2.StashAt()) != 5 {
			t.Fatalf("stash %v at %d", got, ws2.StashAt())
		}
		if ws1.StashLen() != 0 {
			t.Fatalf("ws1 kept %v", seenStash(ws1))
		}
	})
}

// Switching to output scope collects every stash of the monitor, in
// monitor then workspace order, with the current workspace's selection.
func TestStashScopeReloadToOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeWorkspace, "A")
		m := c.cur().mon
		ws1 := m.Current()
		m.AddWindow(1)
		stashTile(ws1, 2)
		stashTile(ws1, 3)
		m.Focus(1)
		ws2 := m.Current()
		m.AddWindow(4)
		stashTile(ws2, 5)
		c.lendStashes()
		if ws1.StashLen() != 2 || ws2.StashLen() != 1 {
			t.Fatal("workspace scope moved stashes")
		}
		c.cfg.Stash.Scope = domain.StashScopeOutput
		c.lendStashes()
		if got := seenStash(ws2); !slices.Equal(got, []WindowID{2, 3, 5}) || ws2.StashAt() != 2 {
			t.Fatalf("stash %v at %d", got, ws2.StashAt())
		}
		if ws1.StashLen() != 0 {
			t.Fatalf("ws1 kept %v", seenStash(ws1))
		}
		c.cfg.Stash.Scope = domain.StashScopeWorkspace
		c.lendStashes()
		if got := seenStash(ws2); !slices.Equal(got, []WindowID{2, 3, 5}) || m.stash.Len() != 0 || m.lendee != nil {
			t.Fatalf("back to workspace: stash %v, monitor %d", got, m.stash.Len())
		}
		m.Focus(0)
		c.lendStashes()
		if ws1.StashLen() != 0 || ws2.StashLen() != 3 {
			t.Fatal("workspace scope moved the stash after reload")
		}
	})
}

func TestOutputStashPerMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A", "B")
		a, b := c.screens[0].mon, c.screens[1].mon
		a.AddWindow(1)
		stashTile(a.Current(), 2)
		b.AddWindow(3)
		stashTile(b.Current(), 4)
		c.lendStashes()
		if !slices.Equal(seenStash(a.Current()), []WindowID{2}) || !slices.Equal(seenStash(b.Current()), []WindowID{4}) {
			t.Fatalf("a %v b %v", seenStash(a.Current()), seenStash(b.Current()))
		}
	})
}

func TestLendStashesAllocs(t *testing.T) {
	c := scopeCore(t, domain.StashScopeOutput, "A", "B")
	m := c.cur().mon
	m.AddWindow(1)
	stashTile(m.Current(), 2)
	m.Focus(1)
	m.AddWindow(3)
	c.lendStashes()
	if n := testing.AllocsPerRun(100, c.lendStashes); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}

// A shared stash stays in place while the workspaces slide under it; the
// workspace's own tiles slide.
func TestOutputStashStaysDuringSwitchSlide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := scopeCore(t, domain.StashScopeOutput, "A")
		m := c.cur().mon
		ws1 := m.Current()
		stashTile(ws1, 2)
		c.lendStashes()
		m.Focus(1)
		ws2 := m.Current()
		ws2.AddWindow(3)
		ws2.FocusID(2)
		c.lendStashes()
		settled := map[WindowID]Rect{}
		for _, p := range ws2.AppendLayout(nil) {
			settled[p.ID] = p.Rect
		}
		m.switchView.Off = -0.5
		moved := map[WindowID]bool{}
		for _, p := range m.Layout() {
			if !p.Hidden {
				moved[p.ID] = p.Rect != settled[p.ID]
			}
		}
		if moved[2] || !moved[3] {
			t.Fatalf("stash moved %v, tile moved %v", moved[2], moved[3])
		}
	})
}
