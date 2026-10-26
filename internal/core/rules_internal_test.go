package core

import (
	"testing"
	"testing/synctest"
)

// Under a covering fullscreen window, a rule's quiet column waits hidden:
// focus, fullscreen and a fixed-overflow maximized column stay as they are,
// as for any new window (AddWindow's cover branch).
func TestAddColumnQuietUnderCover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.Overflow = OverflowFixed
		w.AddWindow(1)
		w.AddWindow(2)
		w.FocusID(1)
		w.Maximize(w.Focus)
		w.ToggleFullscreen()
		if w.cover() != 1 {
			t.Fatalf("setup: cover %d", w.cover())
		}
		w.addColumnQuiet(Column{Windows: []WindowID{3}})
		if !w.has(3) {
			t.Fatal("column not added")
		}
		if id, _ := w.Focused(); id != 1 || w.Fullscreen != 1 {
			t.Fatalf("focus %d fullscreen %d", id, w.Fullscreen)
		}
		if i := w.ColumnOf(1); i < 0 || !w.Columns[i].FullWidth || len(w.Maximized) == 0 || w.Maximized[0] != 1 {
			t.Fatalf("maximized layout lost: %+v %v", w.Columns, w.Maximized)
		}

	})
}
