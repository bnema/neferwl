package core

import (
	"reflect"
	"slices"
	"testing"
)

func TestMoveWindowInColumn(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
		w := workspace()
		w.Overflow = o
		w.Columns = []Column{{Windows: []WindowID{1, 2, 3}, Focus: 1}}
		w.Apply(ActionMoveWindowUp)
		if !slices.Equal(w.Columns[0].Windows, []WindowID{2, 1, 3}) || w.Columns[0].Focus != 0 {
			t.Fatal(o, w.Columns)
		}
		// Top end: nothing moves.
		w.Apply(ActionMoveWindowUp)
		if !slices.Equal(w.Columns[0].Windows, []WindowID{2, 1, 3}) || w.Columns[0].Focus != 0 {
			t.Fatal(o, w.Columns)
		}
		w.Apply(ActionMoveWindowDown)
		w.Apply(ActionMoveWindowDown)
		if !slices.Equal(w.Columns[0].Windows, []WindowID{1, 3, 2}) || w.Columns[0].Focus != 2 {
			t.Fatal(o, w.Columns)
		}
		w.Apply(ActionMoveWindowDown)
		if id, _ := w.Focused(); id != 2 || w.Columns[0].Focus != 2 {
			t.Fatal(o, w.Columns)
		}
	}
}

func TestMoveWindowReleasesSlot(t *testing.T) {
	w := workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2}, Slot: 7}}
	w.Apply(ActionMoveWindowDown)
	if w.Columns[0].Slot != 0 || !slices.Equal(w.Columns[0].Windows, []WindowID{2, 1}) {
		t.Fatal(w.Columns)
	}
	// A window moving into row 0 takes the place of the slot window: the
	// slot is released too.
	w = workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2}, Slot: 7, Focus: 1}}
	w.Apply(ActionMoveWindowUp)
	if w.Columns[0].Slot != 0 {
		t.Fatal(w.Columns)
	}
	// Rows below keep it.
	w = workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2, 3}, Slot: 7, Focus: 1}}
	w.Apply(ActionMoveWindowDown)
	if w.Columns[0].Slot != 7 {
		t.Fatal(w.Columns)
	}
}

func TestMoveWindowOnFloat(t *testing.T) {
	w := workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2}}}
	w.AddFloating(3, 20, 20)
	w.Apply(ActionMoveWindowDown)
	if !slices.Equal(w.Columns[0].Windows, []WindowID{1, 2}) {
		t.Fatal(w.Columns)
	}
}

// set-column-width +N/-N moves cells with no cycle, and a column never
// goes below one cell.
func TestSetColumnWidth(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	w.SetMaxColumns(4)
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddWindow(2)
	before := w.columnWidth(w.Focus)
	w.Apply("set-column-width +1")
	if w.Columns[w.Focus].Cells != 3 || w.columnWidth(w.Focus) <= before {
		t.Fatal(w.Columns, w.columnWidth(w.Focus), before)
	}
	// No donor is left: the width stays, it does not cycle back.
	w.Apply("set-column-width +1")
	if w.Columns[0].Cells != 1 || w.Columns[1].Cells != 3 {
		t.Fatal(w.Columns)
	}
	w.Apply("set-column-width -1")
	if w.Columns[0].Cells != 2 || w.Columns[1].Cells != 2 || w.columnWidth(w.Focus) != before {
		t.Fatal(w.Columns, w.columnWidth(w.Focus), before)
	}
	for range 5 {
		w.Apply("set-column-width -1")
	}
	if w.Columns[1].Cells != 1 || w.Columns[0].Cells != 3 {
		t.Fatal(w.Columns)
	}
	w.Apply("set-column-width +2")
	if w.Columns[1].Cells != 3 || w.Columns[0].Cells != 1 {
		t.Fatal(w.Columns)
	}
}

func TestSetColumnWidthScroll(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetMaxColumns(4)
	w.Overflow = OverflowScroll
	w.AddWindow(1)
	w.AddWindow(2)
	w.Apply("set-column-width +1")
	if w.Columns[w.Focus].Cells != 3 {
		t.Fatal(w.Columns)
	}
	for range 5 {
		w.Apply("set-column-width +1")
	}
	if w.Columns[w.Focus].Cells != 4 {
		t.Fatal("grows up to max-columns", w.Columns)
	}
	for range 9 {
		w.Apply("set-column-width -1")
	}
	if w.Columns[w.Focus].Cells != 1 {
		t.Fatal("never below one cell", w.Columns)
	}
}

func TestSetColumnWidthUnmaximizes(t *testing.T) {
	for _, a := range []Action{"set-column-width +1", "set-column-width -1", ActionCycleColumnWidth} {
		w := workspace()
		w.SetOutput(1000, 800)
		w.SetMaxColumns(4)
		w.AddWindow(1)
		w.ToggleFullWidth()
		w.Apply(a)
		if w.Columns[0].FullWidth {
			t.Fatalf("%s: still maximized", a)
		}
	}
}

func TestColumnEditsUnderFullscreen(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.Columns = []Column{{Windows: []WindowID{1, 2}, Cells: 1}}
	w.SetFullscreen(1, true)
	w.Apply("set-column-width -1")
	w.Apply(ActionCycleColumnWidth)
	w.Apply(ActionMoveWindowDown)
	c := w.Columns[0]
	if c.Cells != 1 || !slices.Equal(c.Windows, []WindowID{1, 2}) {
		t.Fatal(c)
	}
}

func TestSetColumnWidthFixedPastMax(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	w.Apply("set-column-width +1")
	w.Apply("set-column-width -1")
	w.Apply(ActionCycleColumnWidth)
	for _, c := range w.Columns {
		if c.Cells != 0 {
			t.Fatal(w.Columns)
		}
	}
}

func TestMoveWorkspace(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(3)
	m.AddWindow(3)
	// [1:{1}] [2:{2}] [3:{3}] [4:{}]
	m.Apply(ActionMoveWorkspacePrev)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {3}, {2}, {}}) || m.Active != 1 {
		t.Fatal(windows(m), m.Active)
	}
	m.Apply(ActionMoveWorkspacePrev)
	m.Apply(ActionMoveWorkspacePrev)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{3}, {1}, {2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.Apply(ActionMoveWorkspaceNext)
	m.Apply(ActionMoveWorkspaceNext)
	// Never past the trailing empty workspace.
	m.Apply(ActionMoveWorkspaceNext)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {3}, {}}) || m.Active != 2 {
		t.Fatal(windows(m), m.Active)
	}
	// The trailing empty workspace does not move.
	m.FocusNumber(4)
	m.Apply(ActionMoveWorkspacePrev)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {3}, {}}) || m.Active != 3 {
		t.Fatal(windows(m), m.Active)
	}
}

func TestMoveWorkspaceHidden(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev"})
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.ToggleNamed("dev")
	m.Apply(ActionMoveWorkspacePrev)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {}}) {
		t.Fatal(windows(m))
	}
}
