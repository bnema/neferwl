package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
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
