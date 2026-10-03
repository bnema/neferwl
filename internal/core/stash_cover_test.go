package core

import "testing"

// toggle-stash-visible is a user override: under any covering fullscreen
// window, it shows the stash on top, focused, the window fullscreen
// behind; toggling again hides it and the fullscreen window is back.
func TestStashShowsOverCover(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		m := stashMonitor(overflow) // tile 1, stash [2 3]
		w := m.Current()
		w.ToggleStashVisible() // hidden
		w.FocusID(1)
		m.ToggleFullscreen()
		if w.cover() != 1 {
			t.Fatalf("%v: setup cover %d", overflow, w.cover())
		}
		w.ToggleStashVisible()
		ps := w.Layout()
		last := ps[len(ps)-1]
		if id, _ := w.Focused(); id != 3 || w.cover() != 1 {
			t.Fatalf("%v: focused %d cover %d", overflow, id, w.cover())
		}
		if game := previewOf(t, ps, 1); game.Hidden || !game.Fullscreen {
			t.Fatalf("%v: game %+v", overflow, game)
		}
		if p := previewOf(t, ps, 3); p.Hidden || last.ID != 3 && last.ID != 2 {
			t.Fatalf("%v: stash not on top: %+v", overflow, ps)
		}
		// Moves stay in the stash, fullscreen kept.
		w.FocusColumn(-1)
		if id, _ := w.Focused(); id != 2 || w.cover() != 1 {
			t.Fatalf("%v: move focused %d cover %d", overflow, id, w.cover())
		}
		w.ToggleStashVisible()
		if id, _ := w.Focused(); id != 1 || w.cover() != 1 || !previewOf(t, w.Layout(), 2).Hidden {
			t.Fatalf("%v: hide focused %d cover %d", overflow, id, w.cover())
		}
	}
}

// A window mapping under the cover never reaches the stash shown over it
// only by the user, nor does it show: automatic windows stay out.
func TestStashOverCoverNoCapture(t *testing.T) {
	m := stashMonitor(OverflowFixed)
	m.SetStashCapture(true)
	w := m.Current()
	w.ToggleStashVisible()
	w.FocusID(1)
	m.ToggleFullscreen()
	w.ToggleStashVisible()
	m.AddWindow(4)
	if w.stashIndex(4) >= 0 || !previewOf(t, w.Layout(), 4).Hidden {
		t.Fatalf("new window shown: %+v", w.Layout())
	}
}
