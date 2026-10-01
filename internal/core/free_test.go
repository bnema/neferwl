package core

import (
	"slices"
	"testing"
)

func freeWorkspace() *Workspace {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	return w
}

func TestFreeFloatRect(t *testing.T) {
	for _, size := range [][2]int{{1000, 800}, {2000, 1200}} {
		w := freeWorkspace()
		w.SetOutput(size[0], size[1])
		f := Float{ID: 1, W: 200, H: 100, free: true, cx: 0.25, cy: 0.75}
		r := w.floatRect(f)
		u := w.Usable
		if r.X+r.W/2 != u.X+u.W/4 || r.Y+r.H/2 != u.Y+u.H*3/4 || r.W != 200 || r.H != 100 {
			t.Fatal(size, r)
		}
		// Clamped inside the usable area at the edges.
		f.cx, f.cy = 0, 1
		r = w.floatRect(f)
		if r.X != u.X || r.Y+r.H != u.Y+u.H {
			t.Fatal(size, r)
		}
	}
}

func TestToggleFloatingRoundTrip(t *testing.T) {
	w := freeWorkspace()
	w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3}, Focus: 1, Width: Width{Num: 1, Den: 2}}}
	w.Focus = 1
	r, _ := w.placed(3)
	w.Apply(ActionToggleFloating)
	i := w.floatIndex(3)
	if i < 0 || !w.Floats[i].free || !w.floatFocus || w.Floats[i].W != r.W || w.Floats[i].H != r.H {
		t.Fatal(w.Floats, r)
	}
	if got := w.floatRect(w.Floats[i]); got.X+got.W/2 != w.Usable.X+w.Usable.W/2 {
		t.Fatal(got)
	}
	if !w.imposedFloat(3) || len(w.Columns[1].Windows) != 1 {
		t.Fatal(w.Columns)
	}
	// Client reports do not change a free float's size.
	w.ResizeFloating(3, 10, 10)
	if w.Floats[i].W != r.W {
		t.Fatal(w.Floats[i])
	}
	w.Apply(ActionToggleFloating)
	if w.floatIndex(3) >= 0 || !slices.Equal(w.Columns[1].Windows, []WindowID{2, 3}) || w.Columns[1].Focus != 1 || w.Focus != 1 {
		t.Fatal(w.Columns)
	}
	// A lone column comes back at its width and index.
	w.Focus = 1
	w.Columns[1] = Column{Windows: []WindowID{2}, Width: Width{Num: 1, Den: 2}}
	w.Columns = append(w.Columns, Column{Windows: []WindowID{3}})
	w.Apply(ActionToggleFloating)
	w.Apply(ActionToggleFloating)
	if len(w.Columns) != 3 || w.Columns[1].Windows[0] != 2 || w.Columns[1].Width != (Width{Num: 1, Den: 2}) || w.Focus != 1 {
		t.Fatal(w.Columns, w.Focus)
	}
}

func TestToggleFloatingNativeAndNoops(t *testing.T) {
	w := freeWorkspace()
	w.AddWindow(1)
	w.AddFloating(2, 100, 100)
	w.Apply(ActionToggleFloating)
	if w.floatIndex(2) >= 0 || len(w.Columns) != 2 {
		t.Fatal(w.Columns, w.Floats)
	}
	// Stashed: no-op.
	w.Apply(ActionToggleWindowStash)
	id, _ := w.Focused()
	w.Apply(ActionToggleFloating)
	if w.stashIndex(id) < 0 {
		t.Fatal(w.Stash)
	}
	// Fullscreen: no-op.
	w = freeWorkspace()
	w.AddWindow(1)
	w.SetFullscreen(1, true)
	w.Apply(ActionToggleFloating)
	if len(w.Columns) != 1 || len(w.Floats) != 0 {
		t.Fatal(w.Columns)
	}
	// Fixed overflow: the fullscreen window covers the others and keeps
	// the focus, so nothing floats.
	w = freeWorkspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddWindow(2)
	w.SetFullscreen(2, true)
	w.Apply(ActionToggleFloating)
	if len(w.Columns) != 2 || len(w.Floats) != 0 {
		t.Fatal(w.Columns, w.Floats)
	}
}

func TestFreeFloatStash(t *testing.T) {
	w := freeWorkspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.Apply(ActionToggleFloating)
	i := w.floatIndex(2)
	w.Floats[i].cx, w.Floats[i].cy = 0.3, 0.6
	want := w.Floats[i]
	w.Apply(ActionToggleWindowStash)
	if w.stashIndex(2) < 0 || w.floatIndex(2) >= 0 {
		t.Fatal(w.Stash, w.Floats)
	}
	w.Apply(ActionToggleWindowStash)
	if i := w.floatIndex(2); i < 0 || w.Floats[i].cx != 0.3 || w.Floats[i].cy != 0.6 || w.Floats[i].W != want.W || !w.floatFocus {
		t.Fatal(w.Floats)
	}
	// It still returns to its column.
	w.Apply(ActionToggleFloating)
	if len(w.Columns) != 2 || w.Columns[1].Windows[0] != 2 {
		t.Fatal(w.Columns)
	}
}

func TestFreeFloatMovesAcrossWorkspaces(t *testing.T) {
	m := monitor()
	m.SetOutput(1000, 800)
	m.AddWindow(1)
	m.AddWindow(2)
	m.Apply(ActionToggleFloating)
	w := m.Current()
	i := w.floatIndex(2)
	w.Floats[i].cx, w.Floats[i].cy = 0.2, 0.8
	m.MoveToWorkspace(1, false)
	to := m.Workspaces[1]
	if i := to.floatIndex(2); i < 0 || !to.Floats[i].free || to.Floats[i].cx != 0.2 || to.Floats[i].cy != 0.8 {
		t.Fatal(to.Floats)
	}
	// Stashed there, moved back, unstashed: still free at its place.
	m.FocusNumber(2)
	m.Apply(ActionToggleWindowStash)
	m.MoveToWorkspace(0, false)
	m.FocusNumber(1)
	m.Apply(ActionToggleWindowStash)
	w = m.Current()
	if i := w.floatIndex(2); i < 0 || !w.Floats[i].free || w.Floats[i].cx != 0.2 {
		t.Fatal(w.Floats, w.Stash)
	}
}

func TestFreeFloatReturnsAfterWorkspaceMove(t *testing.T) {
	m := monitor()
	m.SetOutput(1000, 800)
	m.AddWindow(1)
	m.AddWindow(2)
	m.Apply(ActionMaximizeColumn)
	m.Apply(ActionToggleFloating)
	m.MoveToWorkspace(1, false)
	m.FocusNumber(2)
	m.Current().FocusID(2)
	m.Apply(ActionToggleFloating)
	w := m.Current()
	if len(w.Columns) != 1 || w.Columns[0].Windows[0] != 2 || w.Columns[0].FullWidth {
		t.Fatal(w.Columns)
	}
}

func TestToggleFloatingReleasesSlot(t *testing.T) {
	w := freeWorkspace()
	w.Columns = []Column{{Windows: []WindowID{1}, Slot: 7}}
	w.Apply(ActionToggleFloating)
	w.Apply(ActionToggleFloating)
	if len(w.Columns) != 1 || w.Columns[0].Slot != 0 {
		t.Fatal(w.Columns)
	}
}

func TestFreeFloatNoUsableArea(t *testing.T) {
	w := freeWorkspace()
	w.AddWindow(1)
	w.Apply(ActionToggleFloating)
	w.SetUsable(Rect{})
	if fx := w.Apply(ActionCloseWindow); fx.Close != 1 {
		t.Fatal(fx)
	}
	w.Apply(ActionMoveColumnLeft)
	w.Apply("set-column-width +10%")
	if w.floatIndex(1) < 0 {
		t.Fatal(w.Floats)
	}
}

func TestFreeFloatKeyboard(t *testing.T) {
	w := freeWorkspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.Apply(ActionToggleFloating)
	i := w.floatIndex(2)
	w.Floats[i].W, w.Floats[i].H = 200, 100
	r := w.floatRect(w.Floats[i])
	w.Apply(ActionMoveColumnLeft)
	w.Apply(ActionMoveWindowDown)
	if got := w.floatRect(w.Floats[i]); got.X != r.X-freeNudge || got.Y != r.Y+freeNudge {
		t.Fatal(got, r)
	}
	for range 50 {
		w.Apply(ActionMoveColumnLeft)
	}
	if got := w.floatRect(w.Floats[i]); got.X != w.Usable.X {
		t.Fatal(got)
	}
	w.Apply("set-column-width +10%")
	w.Apply("set-window-height -10%")
	if w.Floats[i].W != 200+w.Usable.W/10 || w.Floats[i].H != freeMinSize {
		t.Fatal(w.Floats[i])
	}
	// The columns did not change.
	if len(w.Columns) != 1 || w.Columns[0].Width != (Width{}) {
		t.Fatal(w.Columns)
	}
}
