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
	w.Stash = []Float{{ID: 7}}
	w.stashHidden = true
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
