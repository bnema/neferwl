package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// An unchanged rebuild publishes the previous bars: no allocation.
func TestTabBarsUnchangedAllocs(t *testing.T) {
	c := &Core{}
	w := &Workspace{Lanes: layout.Lanes{Overflow: OverflowScroll, MaxColumns: 2}}
	w.SetOutput(300, 100)
	w.TabBar = 20
	w.Columns = []layout.Column{{Windows: []WindowID{1, 2, 3}, Focus: 1, Tabbed: true}}
	placements := w.Layout()
	view := whole(Rect{W: 300, H: 100})
	var s tabBarScratch
	build := func(prev []domain.TabBar) []domain.TabBar {
		s.bars, s.titles = s.bars[:0], s.titles[:0]
		c.appendTabBars(&s, w, placements, view, Rect{})
		return publishTabBars(&s, prev)
	}
	prev := build(nil)
	if len(prev) != 1 || len(prev[0].Tabs) != 3 {
		t.Fatalf("bars %+v", prev)
	}
	if n := testing.AllocsPerRun(100, func() {
		if got := build(prev); &got[0] != &prev[0] {
			t.Fatal("bars not reused")
		}
	}); n != 0 {
		t.Fatalf("unchanged rebuild allocates %v", n)
	}
}

// The bar hit-test finds the tab, the bar between tabs, nothing off it,
// without allocating.
func TestTabAt(t *testing.T) {
	sc := &screen{}
	sc.last.TabBars = []domain.TabBar{{Rect: Rect{X: 10, Y: 0, W: 100, H: 20}, Tabs: []domain.TabTitle{
		{Rect: Rect{X: 10, W: 50, H: 20}, Window: 1}, {Rect: Rect{X: 60, W: 40, H: 20}, Window: 2},
	}}}
	for _, tc := range []struct {
		x, y float64
		id   WindowID
		over bool
	}{{15, 5, 1, true}, {70, 19.5, 2, true}, {105, 5, 0, true}, {5, 5, 0, false}, {50, 20, 0, false}} {
		if id, over := tabAt(sc, tc.x, tc.y); id != tc.id || over != tc.over {
			t.Fatalf("at %v,%v: %d %v", tc.x, tc.y, id, over)
		}
	}
	if n := testing.AllocsPerRun(100, func() { tabAt(sc, 70, 5) }); n != 0 {
		t.Fatalf("tabAt allocates %v", n)
	}
}

// tabCore publishes a tabbed column {1, 2} (2 visible) on a 400x300 output.
func tabCore(t *testing.T, cfg domain.Config) (*Core, chan []domain.Scene) {
	t.Helper()
	scenes := make(chan []domain.Scene, 1)
	commands := make(chan ports.ClientCommand, 128)
	c, err := New(cfg, Channels{Scenes: scenes, Commands: commands}, testOptions(t, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	c.cur().info = domain.OutputInfo{Name: "OUT", Width: 400, Height: 300}
	m := c.cur().mon
	m.SetOutput(400, 300)
	m.SetTabBar(20)
	w := m.Current()
	w.AddWindow(1)
	w.AddWindow(2)
	w.Columns = []layout.Column{{Windows: []WindowID{1, 2}, Focus: 1, Tabbed: true}}
	return c, scenes
}

// A demoted float covering the bar does not take the bar's pointer.
func TestTabBarOverBelowFloat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := domain.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		cfg.Border.Width = 2
		c, scenes := tabCore(t, cfg)
		w := c.cur().mon.Current()
		w.AddFloating(3, 400, 300)
		w.FocusID(2)
		if err := c.publish(context.Background()); err != nil {
			t.Fatal(err)
		}
		s := (<-scenes)[0]
		below := false
		for _, p := range w.Layout() {
			below = below || p.ID == 3 && p.Below
		}
		if !below || len(s.TabBars) != 1 {
			t.Fatalf("setup: below %v bars %+v", below, s.TabBars)
		}
		b := s.TabBars[0].Rect
		if id, _, _ := c.hit(float64(b.X+5), float64(b.Y+b.H/2)); id != 0 {
			t.Fatalf("hit over bar: %d", id)
		}
	})
}

// A cmd press on a tab that cannot be dragged is swallowed and published.
func TestTabCmdClickNotDraggable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := domain.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c, scenes := tabCore(t, cfg)
		ctx := context.Background()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		s := (<-scenes)[0]
		tab := s.TabBars[0].Tabs[0].Rect
		// A cover makes every tile undraggable; the published bars stay.
		w := c.cur().mon.Current()
		w.AddFloating(4, 100, 100)
		w.Fullscreen = 4
		c.cursorX, c.cursorY = float64(tab.X+2), float64(tab.Y+2)
		c.mods = c.cmdMod
		handled, err := c.tabClick(ctx, ports.PointerButton{Button: btnLeft, Pressed: true})
		if err != nil || !handled {
			t.Fatalf("handled %v err %v", handled, err)
		}
		if c.drag != nil || !c.buttons[btnLeft] || !c.swallow[btnLeft] {
			t.Fatalf("drag %v buttons %v swallow %v", c.drag, c.buttons[btnLeft], c.swallow[btnLeft])
		}
		select {
		case <-scenes:
		default:
			t.Fatal("no publish")
		}
	})
}
