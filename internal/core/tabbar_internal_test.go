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
