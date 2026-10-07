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

func TestResizeColumn(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	w.AddWindow(1)
	w.AddWindow(2)
	before := w.columnWidth(w.Focus)
	w.Apply("set-column-width +10%")
	if w.Columns[w.Focus].Width.Den != 100 || w.columnWidth(w.Focus) <= before {
		t.Fatal(w.Columns[w.Focus].Width, w.columnWidth(w.Focus), before)
	}
	w.Apply("set-column-width -10%")
	if d := w.columnWidth(w.Focus) - before; d < -1 || d > 1 {
		t.Fatal(w.columnWidth(w.Focus), before)
	}
	for range 20 {
		w.Apply("set-column-width +10%")
	}
	if w.Columns[w.Focus].Width != (Width{Num: 100, Den: 100}) {
		t.Fatal(w.Columns[w.Focus].Width)
	}
	for range 20 {
		w.Apply("set-column-width -10%")
	}
	if w.Columns[w.Focus].Width != (Width{Num: 10, Den: 100}) {
		t.Fatal(w.Columns[w.Focus].Width)
	}
	// A preset snaps to the nearest whole percent once; steps then
	// round-trip exactly.
	for _, p := range []Width{{Num: 1, Den: 3}, {Num: 1, Den: 2}, {Num: 2, Den: 3}} {
		w.Columns[w.Focus].Width = p
		px := w.columnWidth(w.Focus)
		w.Apply("set-column-width +7%")
		w.Apply("set-column-width -7%")
		snapped := w.columnWidth(w.Focus)
		if d := snapped - px; d < -w.Usable.W/100 || d > w.Usable.W/100 {
			t.Fatal(p, snapped, px)
		}
		w.Apply("set-column-width +7%")
		w.Apply("set-column-width -7%")
		if w.columnWidth(w.Focus) != snapped {
			t.Fatal(p, w.columnWidth(w.Focus), snapped)
		}
	}
}

func TestResizeColumnUnmaximizesAndCycles(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetPresets([]Width{{Num: 1, Den: 2}, {Num: 1, Den: 1}})
	w.AddWindow(1)
	w.ToggleFullWidth()
	w.Apply("set-column-width -10%")
	if w.Columns[0].FullWidth {
		t.Fatal("still maximized")
	}
	// A width outside the presets restarts the cycle at the first preset.
	w.CycleWidth()
	if w.Columns[0].Width != (Width{Num: 1, Den: 2}) {
		t.Fatal(w.Columns[0].Width)
	}
}

func TestResizeColumnSameValueAsPreset(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetPresets([]Width{{Num: 1, Den: 3}, {Num: 1, Den: 2}, {Num: 1, Den: 1}})
	w.AddWindow(1)
	w.Columns[0].Width = Width{Num: 50, Den: 100}
	w.CycleWidth()
	if w.Columns[0].Width != (Width{Num: 1, Den: 1}) {
		t.Fatal(w.Columns[0].Width)
	}
}

func TestColumnEditsUnderFullscreen(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.Columns = []Column{{Windows: []WindowID{1, 2}, Width: Width{Num: 1, Den: 2}}}
	w.SetFullscreen(1, true)
	w.Apply("set-column-width -10%")
	w.Apply(ActionMoveWindowDown)
	c := w.Columns[0]
	if c.Width != (Width{Num: 1, Den: 2}) || !slices.Equal(c.Windows, []WindowID{1, 2}) {
		t.Fatal(c)
	}
}

func TestResizeColumnFixedNoop(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.Apply("set-column-width +10%")
	if w.Columns[0].Width != (Width{}) {
		t.Fatal(w.Columns[0].Width)
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
