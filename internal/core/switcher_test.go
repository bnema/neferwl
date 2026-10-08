package core

import (
	"slices"
	"testing"
)

// threeColumns is a workspace of three one-window columns, windows 1..3.
func threeColumns() *Workspace {
	w := workspace()
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	return w
}

func focusNote(w *Workspace, id WindowID) {
	w.FocusID(id)
	w.noteFocus()
}

func TestMRUOrderFollowsFocus(t *testing.T) {
	w := threeColumns()
	// Columns 0, 2, 1 in turn: the last one used goes first.
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	if got, want := w.switchOrder(), []WindowID{2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	focusNote(w, 1)
	if got, want := w.switchOrder(), []WindowID{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestMRUColumnsNeverFocusedFollowByPosition(t *testing.T) {
	w := threeColumns()
	focusNote(w, 3)
	if got, want := w.switchOrder(), []WindowID{3, 1, 2}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestMRUPrunesClosedWindows(t *testing.T) {
	w := threeColumns()
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	w.RemoveWindow(3)
	if got, want := w.switchOrder(), []WindowID{2, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if slices.Contains(w.recent, 3) {
		t.Fatalf("recent %v keeps a closed window", w.recent)
	}
}

func TestMRUKeepsRankWhenColumnMoves(t *testing.T) {
	w := threeColumns()
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	// Window 2 moves to the right: its column is the same, so is its rank.
	w.MoveColumn(1)
	w.noteFocus()
	if got, want := w.switchOrder(), []WindowID{2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if len(w.recent) != 3 {
		t.Fatalf("recent %v", w.recent)
	}
}

func TestMRUOneWindowPerColumn(t *testing.T) {
	w := threeColumns()
	w.AddWindow(4)
	w.FocusColumn(-1)
	w.ConsumeOrExpel(-1)
	// Whatever the columns became, each appears once.
	for range 3 {
		w.noteFocus()
		w.FocusColumn(-1)
	}
	order := w.switchOrder()
	if len(order) != len(w.Columns) {
		t.Fatalf("order %v for %d columns", order, len(w.Columns))
	}
	seen := map[int]bool{}
	for _, id := range order {
		if seen[w.columnOf(id)] {
			t.Fatalf("order %v lists a column twice", order)
		}
		seen[w.columnOf(id)] = true
	}
}

func TestMRUSwitchOrderNeedsTwoColumns(t *testing.T) {
	w := workspace()
	if w.switchOrder() != nil {
		t.Fatal("order of an empty workspace")
	}
	w.AddWindow(1)
	w.noteFocus()
	if w.switchOrder() != nil {
		t.Fatal("order of one column")
	}
}

func TestMRUCapped(t *testing.T) {
	w := workspace()
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= switcherRecentMax+10; id++ {
		w.AddWindow(id)
		w.noteFocus()
	}
	if len(w.recent) != switcherRecentMax {
		t.Fatalf("recent holds %d", len(w.recent))
	}
}

func TestMRUNoteFocusDoesNotAllocateWhenSettled(t *testing.T) {
	w := threeColumns()
	focusNote(w, 2)
	if n := testing.AllocsPerRun(100, w.noteFocus); n != 0 {
		t.Fatalf("noteFocus allocates %v", n)
	}
}
