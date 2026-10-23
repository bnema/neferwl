package core

import (
	"reflect"
	"slices"
	"testing"
)

// TestLayoutGoldenRects pins the reviewed output of each layout: scroll,
// fixed (row and spiral) and cascade bands, with and without a maximized
// column. The rects are screen coordinates of Usable {40,60,1000,720},
// gap 8, once the focus is scrolled into view.
func TestLayoutGoldenRects(t *testing.T) {
	for _, tc := range []struct {
		name        string
		overflow    Overflow
		k, n, focus int
		maximized   bool
		view        int
		rects       []Rect // settled
		ignoreFull  []Rect // nil: same as rects
		snaps       []float64
	}{
		{name: "scroll", overflow: OverflowScroll, k: 2, n: 4, focus: 3, view: 992,
			rects: []Rect{rc(-944, 68, 488, 704), rc(-448, 68, 488, 704), rc(48, 68, 488, 704), rc(544, 68, 488, 704)},
			snaps: []float64{0, 496, 992}},
		{name: "scroll maximized", overflow: OverflowScroll, k: 2, n: 3, focus: 1, maximized: true, view: 496,
			rects:      []Rect{rc(-448, 68, 488, 704), rc(48, 68, 984, 704), rc(1040, 68, 488, 704)},
			ignoreFull: []Rect{rc(-448, 68, 488, 704), rc(48, 68, 488, 704), rc(544, 68, 488, 704)},
			snaps:      []float64{0, 496, 992}},
		{name: "fixed row", overflow: OverflowFixed, k: 3, n: 3, focus: 1,
			rects: []Rect{rc(48, 68, 322, 704), rc(378, 68, 322, 704), rc(708, 68, 324, 704)},
			snaps: []float64{0}},
		{name: "fixed spiral", overflow: OverflowFixed, k: 2, n: 5, focus: 4,
			rects: []Rect{rc(48, 68, 488, 704), rc(544, 68, 488, 348), rc(544, 424, 240, 348), rc(792, 424, 240, 170), rc(792, 602, 240, 170)},
			snaps: []float64{0, 496, 992, 1488}},
		{name: "fixed spiral maximized", overflow: OverflowFixed, k: 2, n: 4, focus: 0, maximized: true,
			rects:      []Rect{rc(48, 68, 984, 704), rc(1040, 68, 488, 704), rc(1536, 68, 488, 704), rc(2032, 68, 488, 704)},
			ignoreFull: []Rect{rc(48, 68, 488, 704), rc(544, 68, 488, 348), rc(544, 424, 240, 348), rc(792, 424, 240, 348)},
			snaps:      []float64{0, 496, 992, 1488}},
		{name: "cascade bands", overflow: OverflowCascade, k: 2, n: 5, focus: 3, view: 720,
			rects: []Rect{rc(48, -652, 488, 704), rc(544, -652, 488, 704), rc(48, 68, 488, 704), rc(544, 68, 488, 704), rc(48, 788, 984, 704)},
			snaps: []float64{0, 720, 1440}},
		{name: "cascade maximized", overflow: OverflowCascade, k: 3, n: 4, focus: 1, maximized: true, view: 720,
			rects: []Rect{rc(48, -652, 984, 704), rc(48, 68, 984, 704), rc(48, 788, 488, 704), rc(544, 788, 488, 704)},
			snaps: []float64{0, 720, 1440}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := Workspace{Overflow: tc.overflow, MaxColumns: tc.k, Output: Rect{X: 10, Y: 20, W: 1200, H: 900}, Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Gaps: 8}
			for i := 0; i < tc.n; i++ {
				w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
			}
			w.Focus = tc.focus
			w.Columns[tc.focus].FullWidth = tc.maximized
			w.scroll()
			if w.View != tc.view {
				t.Fatalf("view %d, want %d", w.View, tc.view)
			}
			if got := w.columnRectsAt(nil, false, 0); !reflect.DeepEqual(got, tc.rects) {
				t.Fatalf("rects\n got %v\nwant %v", got, tc.rects)
			}
			ignore := tc.ignoreFull
			if ignore == nil {
				ignore = tc.rects
			}
			if got := w.columnRectsAt(nil, true, 0); !reflect.DeepEqual(got, ignore) {
				t.Fatalf("rects ignoring full width\n got %v\nwant %v", got, ignore)
			}
			if got := w.snapPoints(); !reflect.DeepEqual(got, tc.snaps) {
				t.Fatalf("snap points %v, want %v", got, tc.snaps)
			}
		})
	}
}

func rc(x, y, w, h int) Rect { return Rect{X: x, Y: y, W: w, H: h} }

// The swipe staleness check recomputes snap points for every layout into a
// scratch buffer. Cascade, whose points are the bands, allocates nothing;
// the scroll points keep the two scratch slices of the column edges and
// views they always cost.
func TestSwipeStalenessSnapPointsAllocations(t *testing.T) {
	for _, tc := range []struct {
		o      Overflow
		allocs float64
	}{{OverflowScroll, 2}, {OverflowFixed, 2}, {OverflowCascade, 0}} {
		t.Run(string(tc.o), func(t *testing.T) {
			w := Workspace{Overflow: tc.o, MaxColumns: 2, Usable: Rect{W: 1000, H: 720}, Output: Rect{W: 1000, H: 720}, Gaps: 8}
			for i := 0; i < 5; i++ {
				w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
			}
			w.scroll()
			g := swipeGesture{ws: &w, points: w.snapPoints()}
			g.scratch = w.snapPointsInto(g.scratch)
			if allocs := testing.AllocsPerRun(50, func() {
				g.scratch = g.ws.snapPointsInto(g.scratch)
				if !slices.Equal(g.scratch, g.points) {
					t.Fatal("points moved")
				}
			}); allocs != tc.allocs {
				t.Fatalf("staleness check allocates %g times, want %g", allocs, tc.allocs)
			}
		})
	}
}
