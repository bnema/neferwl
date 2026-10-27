package workspace

import (
	"slices"
	"testing"
	"testing/synctest"
)

func order(w *Workspace) []WindowID {
	ids := []WindowID{}
	for _, p := range w.Layout() {
		if !p.Hidden {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func wantOrder(t *testing.T, w *Workspace, want ...WindowID) {
	t.Helper()
	if got := order(w); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestCoveringFloatOrder(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := workspace()
				w.Overflow = overflow
				w.border = 2
				w.AddWindow(1)
				w.AddWindow(2)
				w.AddFloating(3, 98, 79) // placed rect fills the 100x81 usable area
				wantOrder(t, w, 1, 2, 3)
				w.FocusID(2)
				wantOrder(t, w, 3, 1, 2)
				if id, _ := w.Focused(); id != 2 {
					t.Fatalf("focus %d", id)
				}
				w.RemoveWindow(2)
				wantOrder(t, w, 3, 1)
				if id, _ := w.Focused(); id != 1 {
					t.Fatalf("after close focus %d", id)
				}
				w.AddFloating(4, 20, 20) // new dialogs map on top
				wantOrder(t, w, 3, 1, 4)
				w.FocusID(3)
				wantOrder(t, w, 1, 3, 4) // user-selected float rises only within its allowed group
				w.FocusID(1)
				wantOrder(t, w, 3, 1, 4)
				w.ResizeFloating(3, 99, 80)
				wantOrder(t, w, 3, 1, 4)
				w.SetOutput(102, 83)
				w.SetUsable(Rect{W: 102, H: 83})
				wantOrder(t, w, 3, 1, 4)
				w.ResizeFloating(3, 20, 20)
				wantOrder(t, w, 1, 4, 3)

			})
		})
	}
}

func TestCoveringFloatGeometryAndGroup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.border = 2
		w.AddWindow(1)
		w.AddFloating(2, 94, 75) // two border widths plus two pixels on each axis
		if !w.coversFloat(w.Floats[0]) {
			t.Fatal("rounding tolerance")
		}
		w.FocusID(1)
		wantOrder(t, w, 2, 1)
		w.SetUsable(Rect{X: 5, Y: 4, W: 90, H: 73})
		wantOrder(t, w, 2, 1)
		w.SetUsable(Rect{W: 100, H: 81})
		wantOrder(t, w, 2, 1)
		w.border = 0
		w.reconcileFloats()
		wantOrder(t, w, 1, 2)

	})
}

// Directional focus on a covering float with nothing under it keeps the
// float focused.
func TestCoveringFloatAloneKeepsFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.border = 2
		w.AddFloating(3, 98, 79)
		w.FocusColumn(-1)
		w.FocusWindow(1)
		if id, ok := w.Focused(); !ok || id != 3 {
			t.Fatalf("focus %d %v", id, ok)
		}
		wantOrder(t, w, 3)

	})
}
