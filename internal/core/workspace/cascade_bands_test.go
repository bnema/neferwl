package workspace

import (
	"testing"
	"testing/synctest"
)

func TestCascadeMaximizeHidesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 3, 3, false)
		w.FocusID(2)
		w.ToggleFullWidth() // {1} {2*} {3}
		for id := WindowID(1); id <= 3; id++ {
			w.FocusID(id)
			var found bool
			for _, p := range w.Layout() {
				if p.ID == id {
					found = true
					if p.Hidden || p.Rect.W <= 0 || p.Rect.H <= 0 {
						t.Fatalf("window %d not shown: %+v", id, p)
					}
				}
			}
			if !found {
				t.Fatalf("no placement for window %d", id)
			}
		}

	})
}

func TestCascadeSpawnInFocusedBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

	})
}

func TestCascadeMaxColumnsShrinkSplitsBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 4, 3, false) // {1 2 3} {4}
		w.SetMaxColumns(2)
		wantBands(t, w, []WindowID{1, 2}, []WindowID{3}, []WindowID{4})
		w.SetMaxColumns(3)
		wantBands(t, w, []WindowID{1, 2}, []WindowID{3}, []WindowID{4})

	})
}

func TestCascadeQuietAndSlotPlacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 5, 3, false) // {1 2 3} {4 5}
		w.FocusID(4)
		w.addColumnQuiet(Column{Windows: []WindowID{6}})
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{4, 5, 6})
		w.FocusID(2)
		w.addColumnQuiet(Column{Windows: []WindowID{7}})
		wantBands(t, w, []WindowID{1, 2, 3}, []WindowID{7}, []WindowID{4, 5, 6})
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("focus %d, want 2", id)
		}

		_, w = cascadeMovesCore(t, 2, 3, false) // {1 2}
		w.AddSlotWindow(9, 1, 0)
		wantBands(t, w, []WindowID{9, 1, 2})
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("focus %d, want 2", id)
		}

	})
}
