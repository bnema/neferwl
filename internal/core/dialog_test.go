package core

import "testing"

// A dialog of the covering fullscreen window shows over it with the focus;
// other floats stay hidden. A focus move goes back to the fullscreen window
// without leaving fullscreen.
func TestDialogOverFullscreenParent(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(overflow), func(t *testing.T) {
			w := workspace()
			w.Overflow = overflow
			w.AddWindow(1)
			w.AddWindow(2)
			w.FocusID(1)
			w.ToggleFullscreen()
			w.AddFloating(3, 20, 10)  // a dialog of nobody
			w.AddDialog(4, 1, 20, 10) // the dialog of the fullscreen window
			w.AddDialog(5, 2, 20, 10) // the dialog of a hidden window
			wantOrder(t, w, 1, 4)
			if id, _ := w.Focused(); id != 4 {
				t.Fatalf("focus %d, want the dialog", id)
			}
			if !w.FocusColumn(1) {
				t.Fatal("focus move refused")
			}
			if id, _ := w.Focused(); id != 1 || w.fullscreen != 1 {
				t.Fatalf("focus %d fullscreen %d", id, w.fullscreen)
			}
			wantOrder(t, w, 1, 4)
			w.FocusID(4)
			if id, _ := w.Focused(); id != 4 {
				t.Fatalf("focus %d, want the dialog back", id)
			}
			w.RemoveWindow(4)
			if id, _ := w.Focused(); id != 1 || w.fullscreen != 1 {
				t.Fatalf("after close focus %d fullscreen %d", id, w.fullscreen)
			}
			wantOrder(t, w, 1)
		})
	}
}
