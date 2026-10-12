package core

import (
	"slices"
	"testing"
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
			c, w := cascadeMovesCore(t, 5, 3, true)
			w.FocusID(tc.from)
			c.applyAction(tc.action)
			wantColumns(t, w, tc.here...)
			wantColumns(t, screenWorkspace(c, tc.screen), tc.moved...)
			wantFocus(t, c, tc.screen, tc.focusedAfter)
		})
	}
}

// Inside a band, left/right stay on the screen.
func TestCascadeInsideBandStaysOnMonitor(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, true)
	w.FocusID(5)
	c.applyAction(ActionMoveColumnLeft)
	wantColumns(t, w, 1, 2, 3, 5, 4)
	wantFocus(t, c, "M", 5)
	c.applyAction(ActionFocusColumnRight)
	wantFocus(t, c, "M", 4)
}

// With no monitor on that side, left/right never cross into the neighbor
// band: at the band edge they step the workspace (the first has none before
// it; the last is the trailing empty one). up/down cross bands, and
// move-column still crosses in column order.
func TestCascadeBandEdgeWithoutMonitorFocusStepsMoveCrosses(t *testing.T) {
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
	wantColumns(t, w, 1, 2, 4, 3, 5)
	wantFocus(t, c, "M", 4)
	w.FocusID(4)
	c.applyAction(ActionMoveColumnRight)
	wantColumns(t, w, 1, 2, 3, 4, 5)
	wantFocus(t, c, "M", 4)
}

// With one column per band, every column is at both edges of its band.
func TestCascadeOneColumnBandsLeaveForMonitor(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 1, true)
	w.FocusID(2)
	c.applyAction(ActionMoveColumnLeft)
	wantColumns(t, w, 1)
	wantColumns(t, screenWorkspace(c, "L"), 2)
	wantFocus(t, c, "L", 2)
}

// Fixed overflow keeps its column order edge: a non-edge column swaps.
func TestFixedMoveColumnKeepsLinearEdge(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 3, true)
	w.Overflow = OverflowFixed
	w.FocusID(2)
	c.applyAction(ActionMoveColumnLeft)
	wantColumns(t, w, 2, 1)
	wantFocus(t, c, "M", 2)
}

// At the top or bottom of its column, move-window-up/down moves a cascade
// column to the adjacent band, where focus-window-up/down goes; bands
// {1 2 3} {4 5}.
func TestCascadeMoveWindowCrossesBands(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(5)
	c.applyAction(ActionMoveWindowUp)
	wantColumns(t, w, 1, 5, 3, 4, 2)
	wantFocus(t, c, "M", 5)
	c.applyAction(ActionMoveWindowDown)
	wantColumns(t, w, 1, 2, 3, 4, 5)
	wantFocus(t, c, "M", 5)
}

// The last band is partial: the move clamps to its last column, and going
// back up keeps the new offset, as focus does.
func TestCascadeMoveWindowPartialBand(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(3)
	c.applyAction(ActionMoveWindowDown)
	wantColumns(t, w, 1, 2, 5, 4, 3)
	c.applyAction(ActionMoveWindowUp)
	wantColumns(t, w, 1, 3, 5, 4, 2)
	wantFocus(t, c, "M", 3)
}

// No band past the first or last: nothing moves.
func TestCascadeMoveWindowStopsAtOuterBands(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(1)
	c.applyAction(ActionMoveWindowUp)
	wantFocus(t, c, "M", 1)
	w.FocusID(4)
	c.applyAction(ActionMoveWindowDown)
	wantColumns(t, w, 1, 2, 3, 4, 5)
	wantFocus(t, c, "M", 4)
}

// A maximized column leaving its band gives up maximization: the band it
// joins shows all its columns.
func TestCascadeMoveWindowEndsMaximization(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(5)
	w.ToggleFullWidth()
	c.applyAction(ActionMoveWindowUp)
	wantColumns(t, w, 1, 5, 3, 4, 2)
	if w.Columns[1].FullWidth || len(w.maximized) != 0 {
		t.Fatalf("still maximized: full width %t, MRU %v", w.Columns[1].FullWidth, w.maximized)
	}
}

// A fullscreen tile at its band edge leaves for the neighbor monitor and
// stays fullscreen, as a tile there does.
func TestCascadeFullscreenBandEdgeFocusesMonitor(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, true)
	w.FocusID(3)
	w.ToggleFullscreen()
	c.applyAction(ActionFocusColumnRight)
	if got := c.cur().name(); got != "R" {
		t.Fatalf("focused screen %q, want R", got)
	}
	if id, _ := w.Focused(); w.fullscreen != 3 || id != 3 {
		t.Fatalf("fullscreen %d, focused %d on M, want 3 and 3", w.fullscreen, id)
	}
}

// From a native float over the columns, the first move leaves the float
// on this monitor, even with the focused column at its band edge.
func TestCascadeFloatAtBandEdgeStaysOnMonitor(t *testing.T) {
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
}

// A stacked window moves inside its column first, then the column changes
// band.
func TestCascadeMoveWindowStackedFirst(t *testing.T) {
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
	wantColumns(t, w, 5, 2, 3, 1)
	wantFocus(t, c, "M", 5)
}

// A fullscreen window keeps its band.
func TestCascadeMoveWindowFullscreenStays(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(4)
	w.ToggleFullscreen()
	c.applyAction(ActionMoveWindowUp)
	wantColumns(t, w, 1, 2, 3, 4, 5)
}

// Scroll overflow has no bands: up/down stop at the column edge.
func TestScrollMoveWindowStopsAtColumnEdge(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 3, false)
	w.Overflow = OverflowScroll
	w.FocusID(2)
	c.applyAction(ActionMoveWindowUp)
	c.applyAction(ActionMoveWindowDown)
	wantColumns(t, w, 1, 2)
	wantFocus(t, c, "M", 2)
}
