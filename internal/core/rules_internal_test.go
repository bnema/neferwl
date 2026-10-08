package core

import "testing"

// Under a covering fullscreen window, a rule's quiet column waits hidden:
// focus, fullscreen and a fixed-overflow maximized column stay as they are,
// as for any new window (AddWindow's cover branch).
func TestAddColumnQuietUnderCover(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddWindow(2)
	w.FocusID(1)
	w.maximize(w.Focus)
	w.ToggleFullscreen()
	if w.cover() != 1 {
		t.Fatalf("setup: cover %d", w.cover())
	}
	w.addColumnQuiet(Column{Windows: []WindowID{3}})
	if !w.has(3) {
		t.Fatal("column not added")
	}
	if id, _ := w.Focused(); id != 1 || w.fullscreen != 1 {
		t.Fatalf("focus %d fullscreen %d", id, w.fullscreen)
	}
	if i := w.columnOf(1); i < 0 || !w.Columns[i].FullWidth || len(w.maximized) == 0 || w.maximized[0] != 1 {
		t.Fatalf("maximized layout lost: %+v %v", w.Columns, w.maximized)
	}
}
