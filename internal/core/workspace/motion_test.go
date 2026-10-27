package workspace

import (
	"math"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
)

// With no column fully shown (a column wider than the space between the
// gaps), the focus goes to the column at the left edge.
func TestSnapFocusWideColumn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &Workspace{Lanes: layout.Lanes{Output: Rect{W: 800, H: 600}, Usable: Rect{W: 800, H: 600}, Gaps: 10}}
		for i := range 3 {
			w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
		}
		// Full width with gaps: 780 wide, between 10 and 790. The view
		// columnX(1)+1 shifts it 1 px left, so it is not fully shown.
		w.Columns[1].FullWidth = true
		if !workspaceSlidable(w) {
			t.Fatal("the workspace does not scroll")
		}
		view := w.columnX(1) - w.Usable.X - w.Gap() + 1
		for _, forward := range []bool{true, false} {
			if focus := w.snapFocus(view, forward); focus != 1 {
				t.Fatalf("forward=%t: focus %d, want the wide column 1", forward, focus)
			}
		}

	})
}

func TestRetargetKeepsVelocityAndSlowdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(10, 0)
		w := &Workspace{Lanes: layout.Lanes{View: 100}, view: motion.Slide{Off: -300}}
		w.view.Motion = motion.NewMotion(motion.ViewSpring(w.view.Off, 2000), start, 2)
		w.view.Motion.At(start.Add(20 * time.Millisecond))
		v := w.view.Motion.Velocity()
		if v <= 0 {
			t.Fatalf("velocity %v", v)
		}
		w.View = 40
		w.retarget(100)
		if w.view.Off != -240 || !w.view.Motion.On || w.view.Motion.Slow != 2 {
			t.Fatalf("shift %v motion %+v", w.view.Off, w.view.Motion)
		}
		if got := w.view.Motion.Velocity(); got != v {
			t.Fatalf("velocity %v after retarget, want %v", got, v)
		}
		if got := w.view.Motion.Spring.Velocity; math.Abs(got-2*v) > 1e-9 {
			t.Fatalf("spring velocity %v, want %v in spring time", got, 2*v)
		}
		var idle Workspace
		idle.View = 5
		idle.retarget(0)
		if idle.view.Motion.On || idle.view.Off != 0 {
			t.Fatal("retarget without a slide must not start one")
		}

	})
}

func TestSnap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Four columns of 400 on an 800 wide view, no gaps: views align on
		// column edges between 0 and 800.
		w := &Workspace{Lanes: layout.Lanes{Output: Rect{W: 800, H: 600}, Usable: Rect{W: 800, H: 600}, MaxColumns: 2}}
		for i := range 4 {
			w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
		}
		if got := w.Engine().SnapPoints(w.Snapshot(), nil); !slices.Equal(got, []float64{0, 400, 800}) {
			t.Fatalf("snap points %v", got)
		}
		for _, tc := range []struct {
			view    int
			forward bool
			focus   int
		}{
			{view: 0, forward: true, focus: 0},
			{view: 400, forward: true, focus: 2},
			{view: 400, forward: false, focus: 1},
			{view: 800, forward: true, focus: 3},
		} {
			if focus := w.snapFocus(tc.view, tc.forward); focus != tc.focus {
				t.Errorf("snapFocus(%d, %t) = %d; want %d", tc.view, tc.forward, focus, tc.focus)
			}
		}

	})
}

// Column edges closer than snapSpacing merge: a step always shows. The
// last column's end stays, dropping a point close before it.
func TestSnapPointsMergeClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			widths []int
			want   []float64
		}{
			// Views aligning a column: 0, 600, 640 (merged into 600), 1000.
			{[]int{600, 400, 640, 360}, []float64{0, 600, 1000}},
			// 0, 360, 600, 960 and the end 1000: 960 goes, 1000 stays.
			{[]int{600, 360, 400, 640}, []float64{0, 360, 600, 1000}},
			// Columns narrower than the view: one point.
			{[]int{300, 300}, []float64{0}},
		} {
			// 100 cells of 10 px, no gaps: a width in px is px/10 cells.
			w := &Workspace{Lanes: layout.Lanes{Output: Rect{W: 1000, H: 600}, Usable: Rect{W: 1000, H: 600}, MaxColumns: 100}}
			for _, px := range tc.widths {
				w.Columns = append(w.Columns, Column{Cells: px / 10, Windows: []WindowID{WindowID(len(w.Columns) + 1)}})
			}
			if got := w.Engine().SnapPoints(w.Snapshot(), nil); !slices.Equal(got, tc.want) {
				t.Errorf("widths %v: snap points %v, want %v", tc.widths, got, tc.want)
			}
		}

	})
}
