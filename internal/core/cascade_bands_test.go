package core

import (
	"slices"
	"testing"
)

// cycle-column-width in a full band pushes the right neighbor to the band
// below, or the left one up from the last column; the end of the cycle
// brings it back.
func TestCascadeCyclePushesNeighbor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		n      int
		focus  WindowID
		pushed [][]WindowID
	}{
		{"middle pushes right down", 3, 2, [][]WindowID{{1, 2}, {3}}},
		{"left pushes right down", 3, 1, [][]WindowID{{1, 3}, {2}}},
		{"last pushes left up", 3, 3, [][]WindowID{{2}, {1, 3}}},
		{"joins the band below", 5, 2, [][]WindowID{{1, 2}, {3, 4, 5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w := cascadeMovesCore(t, tc.n, 3, false)
			w.FocusID(tc.focus)
			before := bandIDs(w)
			w.Apply(ActionCycleColumnWidth)
			wantBands(t, w, tc.pushed...)
			if id, _ := w.Focused(); id != tc.focus {
				t.Fatalf("focus %d, want %d", id, tc.focus)
			}
			if got := w.Columns[w.Focus].Cells; got != 2 {
				t.Fatalf("focused cells %d, want 2", got)
			}
			w.Apply(ActionCycleColumnWidth)
			wantBands(t, w, before...)
			for _, c := range w.Columns {
				if c.Cells != 0 {
					t.Fatalf("cells not back to equal: %+v", w.Columns)
				}
			}
		})
	}
}

// A pushed column that moved meanwhile stays where it is.
func TestCascadeCycleRestoreSkipsMovedColumn(t *testing.T) {
	c, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(2)
	w.Apply(ActionCycleColumnWidth) // {1 2} {3}
	w.FocusID(3)
	c.applyAction(ActionMoveWindowUp) // {3 1 2}: 3 is back by hand.
	w.FocusID(2)
	w.Apply(ActionCycleColumnWidth)
	wantBands(t, w, []WindowID{3, 1, 2})
	if got := cellsOf(w); !slices.Equal(got, []int{0, 0, 0}) {
		t.Fatalf("cells %v, want equal", got)
	}
}

// maximize-column isolates the column: the left ones go up, the right ones
// down, together; maximizing again forms the band back.
func TestCascadeMaximizeSplitsAndRestoresBand(t *testing.T) {
	for _, tc := range []struct {
		name  string
		n, k  int
		focus WindowID
		split [][]WindowID
	}{
		{"middle", 3, 3, 2, [][]WindowID{{1}, {2}, {3}}},
		{"first", 3, 3, 1, [][]WindowID{{1}, {2, 3}}},
		{"left side moves together", 4, 4, 3, [][]WindowID{{1, 2}, {3}, {4}}},
		{"left joins the band above", 5, 3, 5, [][]WindowID{{1, 2, 3}, {4}, {5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w := cascadeMovesCore(t, tc.n, tc.k, false)
			w.FocusID(tc.focus)
			before := bandIDs(w)
			w.ToggleFullWidth()
			wantBands(t, w, tc.split...)
			if !w.Columns[w.Focus].FullWidth {
				t.Fatal("not maximized")
			}
			w.ToggleFullWidth()
			wantBands(t, w, before...)
			if w.Columns[w.Focus].FullWidth {
				t.Fatal("still maximized")
			}
		})
	}
}

// Columns on the left that fit the band above join it at its end.
func TestCascadeMaximizeLeftJoinsBandAbove(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false) // {1 2 3} {4 5}
	w.FocusID(2)
	c.applyAction(ActionMoveWindowDown) // {1 3} {4 2 5}
	w.FocusID(2)
	w.ToggleFullWidth()
	wantBands(t, w, []WindowID{1, 3, 4}, []WindowID{2}, []WindowID{5})
	w.ToggleFullWidth()
	wantBands(t, w, []WindowID{1, 3}, []WindowID{4, 2, 5})
}

// No column is hidden by a maximized cascade column.
func TestCascadeMaximizeHidesNothing(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(2)
	w.ToggleFullWidth()
	for _, p := range w.Layout() {
		if p.Hidden && p.ID != 0 {
			// Off-screen bands are laid out, not hidden by maximize.
			if w.hiddenByMaximized(w.columnOf(p.ID)) {
				t.Fatalf("window %d hidden by maximize", p.ID)
			}
		}
	}
}

// A new window joins the focused band when it has room, else it starts a
// band of its own right below it; other bands do not move.
func TestCascadeSpawnInFocusedBand(t *testing.T) {
	_, w := cascadeMovesCore(t, 5, 3, false) // {1 2 3} {4 5}
	w.FocusID(4)
	w.AddWindow(6)
	wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4, 5, 6})
	w.FocusID(2)
	w.AddWindow(7)
	wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{7}, []WindowID{4, 5, 6})
	if id, _ := w.Focused(); id != 7 {
		t.Fatalf("focus %d, want 7", id)
	}
	w.AddWindow(8)
	wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{7, 8}, []WindowID{4, 5, 6})
}

// A band keeps its columns: closing one leaves the others in place, and a
// band left empty goes away.
func TestCascadeCloseKeepsBands(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(5)
	c.applyAction(ActionMoveWindowDown) // {1 2 3} {4} {5}
	w.RemoveWindow(2)
	wantBands(t, w, []WindowID{1, 3}, []WindowID{4}, []WindowID{5})
	w.RemoveWindow(4)
	wantBands(t, w, []WindowID{1, 3}, []WindowID{5})
}

// A smaller max-columns sends the extra columns of a band to a new band
// right below it; a larger one keeps the bands.
func TestCascadeMaxColumnsShrinkSplitsBand(t *testing.T) {
	_, w := cascadeMovesCore(t, 4, 3, false) // {1 2 3} {4}
	w.SetMaxColumns(2)
	wantBands(t, w, []WindowID{1, 2}, []WindowID{3}, []WindowID{4})
	w.SetMaxColumns(3)
	wantBands(t, w, []WindowID{1, 2}, []WindowID{3}, []WindowID{4})
}

// Switching to cascade from another overflow packs the columns in order.
func TestCascadeOverflowSwitchRepacks(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(900, 600)
	m.SetMaxColumns(3)
	m.SetOverflow(OverflowCascade)
	for id := WindowID(1); id <= 4; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	w.FocusID(2)
	w.ToggleFullWidth() // {1} {2*} {3 4}
	m.SetOverflow(OverflowScroll)
	m.SetOverflow(OverflowCascade)
	wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4})
}

// Moves inside a band keep the band starts in place.
func TestCascadeMoveColumnKeepsBandStarts(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(4)
	c.applyAction(ActionMoveColumnRight)
	wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{5, 4})
}

func bandIDs(w *Workspace) [][]WindowID {
	var out [][]WindowID
	w.walkBands(func(_, first, last int) bool {
		var b []WindowID
		for _, c := range w.Columns[first : last+1] {
			b = append(b, c.Windows[0])
		}
		out = append(out, slices.Clone(b))
		return true
	})
	return out
}
