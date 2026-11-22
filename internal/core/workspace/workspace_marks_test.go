package workspace

import (
	"slices"
	"testing"
)

func TestMarkArg(t *testing.T) {
	for _, tc := range []struct {
		action Action
		op     MarkOp
		name   string
		cmd    []string
		ok     bool
	}{
		{"mark a", MarkSet, "a", nil, true},
		{"toggle-mark a", MarkToggle, "a", nil, true},
		{"unmark a", MarkUnmark, "a", nil, true},
		{"unmark", MarkUnmark, "", nil, true},
		{"focus-mark a", MarkFocus, "a", nil, true},
		{"focus-mark a foot -e x", MarkFocus, "a", []string{"foot", "-e", "x"}, true},
		{"swap-mark a", MarkSwap, "a", nil, true},
		{"move-to-mark a", MarkMoveTo, "a", nil, true},
		{"mark", 0, "", nil, false},
		{"mark a b", 0, "", nil, false},
		{"mark a:b", 0, "", nil, false},
		{"swap-mark", 0, "", nil, false},
		{"unmark a b", 0, "", nil, false},
		{"marks a", 0, "", nil, false},
	} {
		op, name, cmd, ok := MarkArg(tc.action)
		if op != tc.op || name != tc.name || !slices.Equal(cmd, tc.cmd) || ok != tc.ok {
			t.Errorf("MarkArg(%q) = %v %q %q %v", tc.action, op, name, cmd, ok)
		}
	}
}

type layoutSnap struct {
	cols   [][]WindowID
	floats []Float
	stash  []Float
}

func snap(w *Workspace) layoutSnap {
	s := layoutSnap{floats: slices.Clone(w.Floats), stash: slices.Clone(w.Stash)}
	for _, c := range w.Columns {
		s.cols = append(s.cols, slices.Clone(c.Windows))
	}
	return s
}

func sameSnap(a, b layoutSnap) bool {
	return slices.EqualFunc(a.cols, b.cols, slices.Equal) && slices.Equal(a.floats, b.floats) && slices.Equal(a.stash, b.stash)
}

func TestSwapWindowsTiles(t *testing.T) {
	w := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2, 3})
	if !SwapWindows(w, 1, w, 3) {
		t.Fatal("refused")
	}
	if got := ids(w); !slices.EqualFunc(got, [][]WindowID{{3}, {2, 1}}, slices.Equal) {
		t.Fatalf("columns %v", got)
	}
	if id, _ := w.Focused(); id != 3 {
		t.Fatalf("focused %d, want the marked 3", id)
	}
	a, b := cols(OverflowScroll, 3, 0, []WindowID{1}), cols(OverflowScroll, 3, 0, []WindowID{2}, []WindowID{3})
	if !SwapWindows(a, 1, b, 3) || !slices.Equal(a.Columns[0].Windows, []WindowID{3}) || !slices.Equal(b.Columns[1].Windows, []WindowID{1}) {
		t.Fatalf("cross: %v %v", ids(a), ids(b))
	}
}

func TestSwapWindowsFloats(t *testing.T) {
	w := cols(OverflowScroll, 3, 0, []WindowID{9})
	w.Floats = []Float{{ID: 1, W: 100, H: 50}, {ID: 2, W: 300, H: 200}}
	if !SwapWindows(w, 1, w, 2) {
		t.Fatal("refused")
	}
	if w.Floats[0] != (Float{ID: 2, W: 100, H: 50}) || w.Floats[1] != (Float{ID: 1, W: 300, H: 200}) {
		t.Fatalf("floats %+v", w.Floats)
	}
}

func TestSwapWindowsRefused(t *testing.T) {
	build := func() *Workspace {
		w := cols(OverflowScroll, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}, []WindowID{4})
		w.Floats = []Float{{ID: 5, W: 10, H: 10}, {ID: 6, W: 10, H: 10, over: 4}, {ID: 8, W: 10, H: 10}}
		w.Stash = []Float{{ID: 7}}
		w.Fullscreen = 3
		return w
	}
	for _, tc := range []struct {
		name string
		a, b WindowID
	}{
		{"stash", 1, 7},
		{"fullscreen", 1, 3},
		{"dialog", 5, 6},
		{"parent of a dialog", 1, 4},
		{"float and tile", 1, 5},
		{"same window", 1, 1},
	} {
		w := build()
		before := snap(w)
		if SwapWindows(w, tc.a, w, tc.b) || SwapWindows(w, tc.b, w, tc.a) {
			t.Errorf("%s: swapped", tc.name)
		}
		if !sameSnap(before, snap(w)) {
			t.Errorf("%s: layout changed", tc.name)
		}
	}
}

func TestMoveTileBelow(t *testing.T) {
	// Into another column, below the target.
	w := cols(OverflowScroll, 3, 0, []WindowID{1, 2}, []WindowID{3, 4})
	w.Columns[0].Focus = 0
	if !MoveTileBelow(w, 1, w, 3) {
		t.Fatal("refused")
	}
	if got := ids(w); !slices.EqualFunc(got, [][]WindowID{{2}, {3, 1, 4}}, slices.Equal) {
		t.Fatalf("columns %v", got)
	}
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("focused %d", id)
	}
	// Reorder in the same column.
	w = cols(OverflowScroll, 3, 0, []WindowID{1, 2, 3})
	w.Columns[0].Focus = 0
	if !MoveTileBelow(w, 1, w, 3) || !slices.Equal(w.Columns[0].Windows, []WindowID{2, 3, 1}) {
		t.Fatalf("reorder %v", ids(w))
	}
	// A lone source column goes; the target is found again by ID.
	w = cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2, 3})
	if !MoveTileBelow(w, 1, w, 2) {
		t.Fatal("lone refused")
	}
	if got := ids(w); !slices.EqualFunc(got, [][]WindowID{{2, 1, 3}}, slices.Equal) {
		t.Fatalf("lone %v", got)
	}
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("lone focused %d", id)
	}
	// Across workspaces.
	a, b := cols(OverflowScroll, 3, 0, []WindowID{1}), cols(OverflowScroll, 3, 0, []WindowID{2})
	if !MoveTileBelow(a, 1, b, 2) || len(a.Columns) != 0 || !slices.Equal(b.Columns[0].Windows, []WindowID{2, 1}) {
		t.Fatalf("cross %v %v", ids(a), ids(b))
	}
	// A float target is refused.
	w = cols(OverflowScroll, 3, 0, []WindowID{1})
	w.Floats = []Float{{ID: 5}}
	w.floatFocus = false
	before := snap(w)
	if MoveTileBelow(w, 1, w, 5) || !sameSnap(before, snap(w)) {
		t.Fatal("float target moved")
	}
}
