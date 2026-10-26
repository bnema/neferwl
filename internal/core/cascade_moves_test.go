package core

import (
	"slices"
	"testing"
	"testing/synctest"
)

// cascadeMovesCore is a core whose focused screen holds a cascade of
// windows 1..n in bands of maxCols. With sides, screens L and R sit on
// either side of it; otherwise it is alone.
func cascadeMovesCore(t *testing.T, n, maxCols int, sides bool) (*Core, *Workspace) {
	t.Helper()
	tiles := []tile{{"M", 900, 0, 900, 600}}
	focus := 0
	if sides {
		tiles = []tile{{"L", 0, 0, 900, 600}, tiles[0], {"R", 1800, 0, 900, 600}}
		focus = 1
	}
	c := layoutCore(focus, tiles...)
	w := c.cur().mon.Current()
	w.Overflow = OverflowCascade
	w.SetMaxColumns(maxCols)
	for id := WindowID(1); id <= WindowID(n); id++ {
		w.AddWindow(id)
	}
	c.cur().mon.normalize() // the trailing empty workspace, as in a session
	return c, w
}

// columnOrder is the first window of each column, in column order.
func columnOrder(w *Workspace) []WindowID {
	var ids []WindowID
	for _, c := range w.Columns {
		ids = append(ids, c.Windows[0])
	}
	return ids
}

func screenWorkspace(c *Core, name string) *Workspace {
	for _, s := range c.screens {
		if s.name() == name {
			return s.mon.Current()
		}
	}
	return nil
}

func wantColumns(t *testing.T, w *Workspace, want ...WindowID) {
	t.Helper()
	if got := columnOrder(w); !slices.Equal(got, want) {
		t.Fatalf("columns %v, want %v", got, want)
	}
}

// wantBands checks the cascade bands, each by the first window of its
// columns.
func wantBands(t *testing.T, w *Workspace, want ...[]WindowID) {
	t.Helper()
	var got [][]WindowID
	w.WalkBands(func(_, first, last int) bool {
		got = append(got, columnOrder(&Workspace{Lanes: Lanes{Columns: w.Columns[first : last+1]}}))
		return true
	})
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("bands %v, want %v", got, want)
	}
}

func wantFocus(t *testing.T, c *Core, screen string, id WindowID) {
	t.Helper()
	if got := c.cur().name(); got != screen {
		t.Fatalf("focused screen %q, want %q", got, screen)
	}
	if got, _ := c.cur().mon.Current().Focused(); got != id {
		t.Fatalf("focused window %d, want %d", got, id)
	}
}

// At the edge of its band, a cascade column goes to the neighbor monitor
// on that side, and focus with it; bands {1 2 3} {4 5}.
func TestCascadeBandEdgeMovesToNeighborMonitor(t *testing.T) {
	for _, tc := range []struct {
		name         string
		from         WindowID
		action       Action
		screen       string
		here, moved  []WindowID
		focusedAfter WindowID
	}{
		{"move left from band start", 4, ActionMoveColumnLeft, "L", []WindowID{1, 2, 3, 5}, []WindowID{4}, 4},
		{"move right from band end", 3, ActionMoveColumnRight, "R", []WindowID{1, 2, 4, 5}, []WindowID{3}, 3},
		{"move right from last column", 5, ActionMoveColumnRight, "R", []WindowID{1, 2, 3, 4}, []WindowID{5}, 5},
		{"focus left from band start", 4, ActionFocusColumnLeft, "L", []WindowID{1, 2, 3, 4, 5}, nil, 0},
		{"focus right from band end", 3, ActionFocusColumnRight, "R", []WindowID{1, 2, 3, 4, 5}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, w := cascadeMovesCore(t, 5, 3, true)
				w.FocusID(tc.from)
				c.applyAction(tc.action)
				wantColumns(t, w, tc.here...)
				wantColumns(t, screenWorkspace(c, tc.screen), tc.moved...)
				wantFocus(t, c, tc.screen, tc.focusedAfter)

			})
		})
	}
}

// Inside a band, left/right stay on the screen.
func TestCascadeInsideBandStaysOnMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, true)
		w.FocusID(5)
		c.applyAction(ActionMoveColumnLeft)
		wantColumns(t, w, 1, 2, 3, 5, 4)
		wantFocus(t, c, "M", 5)
		c.applyAction(ActionFocusColumnRight)
		wantFocus(t, c, "M", 4)

	})
}

// With no monitor on that side, left/right never cross into the neighbor
// band: at the band edge focus steps the workspace (the first has none
// before it; the last is the trailing empty one) and move-column does
// nothing. up/down cross bands.
func TestCascadeBandEdgeWithoutMonitorFocusStepsMoveStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, false)
		m := c.cur().mon
		w.FocusID(4)
		c.applyAction(ActionFocusColumnLeft)
		wantFocus(t, c, "M", 4)
		if m.Active != 0 {
			t.Fatalf("active %d, want 0", m.Active)
		}
		w.FocusID(3)
		c.applyAction(ActionFocusColumnRight)
		if m.Active != 1 || m.Current() == w {
			t.Fatalf("active %d, want the trailing empty workspace", m.Active)
		}
		m.Focus(0)
		w.FocusID(4)
		c.applyAction(ActionMoveColumnLeft)
		wantColumns(t, w, 1, 2, 3, 4, 5)
		wantFocus(t, c, "M", 4)
		w.FocusID(3)
		c.applyAction(ActionMoveColumnRight)
		wantColumns(t, w, 1, 2, 3, 4, 5)
		wantFocus(t, c, "M", 3)

	})
}

// With one column per band, every column is at both edges of its band.
func TestCascadeOneColumnBandsLeaveForMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 2, 1, true)
		w.FocusID(2)
		c.applyAction(ActionMoveColumnLeft)
		wantColumns(t, w, 1)
		wantColumns(t, screenWorkspace(c, "L"), 2)
		wantFocus(t, c, "L", 2)

	})
}

// Fixed overflow keeps its column order edge: a non-edge column swaps.
func TestFixedMoveColumnKeepsLinearEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 2, 3, true)
		w.Overflow = OverflowFixed
		w.FocusID(2)
		c.applyAction(ActionMoveColumnLeft)
		wantColumns(t, w, 2, 1)
		wantFocus(t, c, "M", 2)

	})
}

// At the top or bottom of its column, move-window-up/down sends a cascade
// column to the adjacent band: it joins it when it has room, at its offset
// in the band, else it makes a new band between the two.
func TestCascadeMoveWindowCrossesBands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, false) // {1 2 3} {4 5}
		w.FocusID(2)
		c.applyAction(ActionMoveWindowDown)
		wantBands(t, w, []WindowID{1, 3}, []WindowID{4, 2, 5})
		wantFocus(t, c, "M", 2)
		c.applyAction(ActionMoveWindowUp)
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4, 5})
		wantFocus(t, c, "M", 2)

	})
}

// A full band cannot take the column: a new band goes between the two,
// and the one it left keeps the others.
func TestCascadeMoveWindowIntoFullBandMakesBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 6, 3, false) // {1 2 3} {4 5 6}
		w.FocusID(5)
		c.applyAction(ActionMoveWindowUp)
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{5}, []WindowID{4, 6})
		wantFocus(t, c, "M", 5)
		// Alone in its band, it passes the full band above.
		c.applyAction(ActionMoveWindowUp)
		wantBands(t, w, []WindowID{5}, []WindowID{1, 2, 3}, []WindowID{4, 6})
		wantFocus(t, c, "M", 5)

	})
}

// Past the outer bands a column sharing its band starts a new one; a
// column alone there does not move.
func TestCascadeMoveWindowOuterBands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, false) // {1 2 3} {4 5}
		w.FocusID(5)
		c.applyAction(ActionMoveWindowDown)
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4}, []WindowID{5})
		c.applyAction(ActionMoveWindowDown)
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4}, []WindowID{5})
		w.FocusID(1)
		c.applyAction(ActionMoveWindowUp)
		wantBands(t, w, []WindowID{1}, []WindowID{2, 3}, []WindowID{4}, []WindowID{5})
		wantFocus(t, c, "M", 1)

	})
}

// A band left empty goes away.
func TestCascadeMoveWindowDropsEmptyBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 4, 3, false) // {1 2 3} {4}
		w.FocusID(2)
		c.applyAction(ActionMoveWindowDown)
		wantBands(t, w, []WindowID{1, 3}, []WindowID{4, 2})
		w.FocusID(4)
		c.applyAction(ActionMoveWindowUp) // At its offset in the band: first.
		wantBands(t, w, []WindowID{4, 1, 3}, []WindowID{2})

	})
}

// A maximized column keeps its band to itself: moving it, or moving a
// column toward it, makes a new band; repeating the move passes it.
func TestCascadeMoveWindowAndMaximizedBand(t *testing.T) {
	for _, tc := range []struct {
		name       string
		n          int
		max, focus WindowID
		action     Action
		want       [][]WindowID
	}{
		// After maximizing 2: {1} {2*} {3 4}.
		{"maximized down", 4, 2, 2, ActionMoveWindowDown, [][]WindowID{{1}, {3, 4}, {2}}},
		{"maximized up", 4, 2, 2, ActionMoveWindowUp, [][]WindowID{{2}, {1}, {3, 4}}},
		// After maximizing 4: {1 2 3} {4*} {5}.
		{"into maximized below", 5, 4, 3, ActionMoveWindowDown, [][]WindowID{{1, 2}, {3}, {4}, {5}}},
		{"into maximized above", 5, 4, 5, ActionMoveWindowUp, [][]WindowID{{1, 2, 3}, {5}, {4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, w := cascadeMovesCore(t, tc.n, 3, false)
				w.FocusID(tc.max)
				w.ToggleFullWidth()
				w.FocusID(tc.focus)
				c.applyAction(tc.action)
				wantBands(t, w, tc.want...)
				wantFocus(t, c, "M", tc.focus)
				if !w.Columns[w.ColumnOf(tc.max)].FullWidth {
					t.Fatal("maximization lost")
				}

			})
		})
	}
}

// A fullscreen tile at its band edge leaves for the neighbor monitor and
// stays fullscreen, as a tile there does.
func TestCascadeFullscreenBandEdgeFocusesMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, true)
		w.FocusID(3)
		w.ToggleFullscreen()
		c.applyAction(ActionFocusColumnRight)
		if got := c.cur().name(); got != "R" {
			t.Fatalf("focused screen %q, want R", got)
		}
		if id, _ := w.Focused(); w.Fullscreen != 3 || id != 3 {
			t.Fatalf("fullscreen %d, focused %d on M, want 3 and 3", w.Fullscreen, id)
		}

	})
}

// From a native float over the columns, the first move leaves the float
// on this monitor, even with the focused column at its band edge.
func TestCascadeFloatAtBandEdgeStaysOnMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, true)
		w.FocusID(3)
		w.AddFloating(9, 100, 100)
		c.applyAction(ActionFocusColumnRight)
		if got := c.cur().name(); got != "M" {
			t.Fatalf("focused screen %q, want M", got)
		}
		if w.floatFocus {
			t.Fatal("focus still on the float")
		}

	})
}

// A stacked window moves inside its column first, then the column changes
// band.
func TestCascadeMoveWindowStackedFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, false)
		w.FocusID(5)
		w.ConsumeOrExpel(-1) // bands {1 2 3} {4/5}
		if got := w.Columns[3].Windows; !slices.Equal(got, []WindowID{4, 5}) {
			t.Fatalf("stack %v, want [4 5]", got)
		}
		c.applyAction(ActionMoveWindowUp)
		if got := w.Columns[3].Windows; !slices.Equal(got, []WindowID{5, 4}) {
			t.Fatalf("stack %v, want [5 4]", got)
		}
		c.applyAction(ActionMoveWindowUp)
		// The full band above cannot take it, so it passes it.
		wantBands(t, w, []WindowID{5}, []WindowID{1, 2, 3})
		wantFocus(t, c, "M", 5)

	})
}

// A fullscreen window keeps its own band (it left {4 5} when it went
// fullscreen).
func TestCascadeMoveWindowFullscreenStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 5, 3, false)
		w.FocusID(4)
		w.ToggleFullscreen()
		c.applyAction(ActionMoveWindowUp)
		wantColumns(t, w, 1, 2, 3, 5, 4)

	})
}

// Scroll overflow has no bands: up/down stop at the column edge.
func TestScrollMoveWindowStopsAtColumnEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := cascadeMovesCore(t, 2, 3, false)
		w.Overflow = OverflowScroll
		w.FocusID(2)
		c.applyAction(ActionMoveWindowUp)
		c.applyAction(ActionMoveWindowDown)
		wantColumns(t, w, 1, 2)
		wantFocus(t, c, "M", 2)

	})
}
