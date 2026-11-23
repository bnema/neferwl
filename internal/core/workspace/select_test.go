package workspace

import "testing"

// Select keeps a maximized column of a fixed layout and does not scroll.
func TestSelectKeepsMaximizeAndView(t *testing.T) {
	w := cols(OverflowFixed, 3, 1, []WindowID{1}, []WindowID{2}, []WindowID{3})
	w.Columns[1].FullWidth = true
	view := w.View
	if !w.Select(1) {
		t.Fatal("refused")
	}
	if id, _ := w.Focused(); id != 1 || !w.Columns[1].FullWidth || w.View != view {
		t.Fatalf("focused %d, maximized %v, view %d", id, w.Columns[1].FullWidth, w.View)
	}
	w.FocusID(2)
	if w.FocusID(1); w.Columns[1].FullWidth {
		t.Fatal("FocusID kept the maximize: the test does not tell them apart")
	}
}

// Select of a hidden stash window refuses; a float is selected.
func TestSelectStashAndFloat(t *testing.T) {
	w := cols(OverflowScroll, 3, 0, []WindowID{1})
	w.Floats = []Float{{ID: 5, W: 10, H: 10}}
	w.st().entries = []Float{{ID: 7}}
	w.st().hidden = true
	if w.Select(7) || w.Select(99) {
		t.Fatal("selected a hidden or missing window")
	}
	if !w.Select(5) {
		t.Fatal("float refused")
	}
	if id, _ := w.Focused(); id != 5 {
		t.Fatalf("focused %d", id)
	}
	if !w.Select(1) {
		t.Fatal("tile refused")
	}
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("focused %d", id)
	}
}

// Select raises a float without ending a stash shown over the cover, and
// refuses a held float.
func TestSelectFloatIsQuiet(t *testing.T) {
	w := cols(OverflowScroll, 3, 0, []WindowID{1})
	w.Floats = []Float{{ID: 5, W: 10, H: 10, below: true}, {ID: 6, W: 10, H: 10}}
	w.st().entries = []Float{{ID: 7}}
	w.stashOver = 1
	if !w.Select(5) || w.stashOver != 1 {
		t.Fatalf("stashOver %d", w.stashOver)
	}
	if last := w.Floats[len(w.Floats)-1]; last.ID != 5 || last.below || !w.floatFocus {
		t.Fatalf("float not raised: %+v", w.Floats)
	}
	if w.FocusID(6); w.stashOver != 0 {
		t.Fatal("FocusID kept the stash over: the test does not tell them apart")
	}
	h := cols(OverflowScroll, 3, 0, []WindowID{1})
	h.Floats = []Float{{ID: 5, W: 10, H: 10}}
	h.Fullscreen = 1
	h.hold(5)
	if !h.IsHeld(5) {
		t.Fatal("setup: 5 not held")
	}
	if h.Select(5) || h.Fullscreen != 1 {
		t.Fatalf("held float selected, cover %d", h.Fullscreen)
	}
}
