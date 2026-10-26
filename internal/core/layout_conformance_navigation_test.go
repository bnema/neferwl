package core

import (
	"testing"
	"testing/synctest"
)

// Navigation invariants: the lanes partition the columns in order, laneNeighbor
// goes one lane over, focus steps stay in their lane (walking a whole lane
// outside the spiral) and terminate, and the screen neighbor is never the focus.
func TestLayoutNavigationConformance(t *testing.T) {
	forEachConformance(t, func(t *testing.T, l Layout, w *Workspace, k, n int, mode string) {
		v := w.Snapshot()
		next := 0
		for lane := 0; lane < l.LaneCount(v); lane++ {
			first, last := l.LaneBounds(v, lane)
			if first != next || last < first || last >= n {
				t.Fatalf("invalid partition %d: %d..%d, next %d", lane, first, last, next)
			}
			for i := first; i <= last; i++ {
				if l.LaneOf(v, i) != lane {
					t.Fatal("laneOf disagrees with bounds")
				}
			}
			// Focus traverses every column of a lane, except in the spiral,
			// where it follows screen adjacency.
			if !l.Spiral() {
				for _, dir := range []int{-1, 1} {
					step := v
					step.Focus = first
					if dir < 0 {
						step.Focus = last
					}
					visited := 1
					for col := l.FocusStep(step, dir); col >= 0; col = l.FocusStep(step, dir) {
						if col < first || col > last || visited > last-first {
							t.Fatal("focus escaped lane or cycled")
						}
						visited++
						step.Focus = col
					}
					if visited != last-first+1 {
						t.Fatal("focus did not visit whole lane")
					}
				}
			}
			next = last + 1
		}
		if next != n {
			t.Fatalf("lanes cover %d of %d columns", next, n)
		}
		for i := 0; i < n; i++ {
			v.Focus = i
			for _, dir := range []int{-1, 1} {
				if col := l.LaneStep(v, dir); col >= n {
					t.Fatal("laneStep out of range")
				}
				if col := l.LaneNeighbor(v, dir); col != -1 && (col >= n || l.LaneOf(v, col) != l.LaneOf(v, i)+dir) {
					t.Fatalf("lane neighbor %d of column %d, direction %d", col, i, dir)
				}
				step := v
				for count := 0; ; count++ {
					if count > n {
						t.Fatal("focusStep does not terminate")
					}
					col := l.FocusStep(step, dir)
					if col < 0 {
						break
					}
					if col >= n || col == step.Focus {
						t.Fatal("invalid focus step")
					}
					step.Focus = col
				}
			}
			rects := l.ColumnRects(v, nil, RectOpts{})
			for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				if col := screenNeighbor(v, rects, d[0], d[1]); col == i || col >= n {
					t.Fatalf("screen neighbor %d of column %d", col, i)
				}
			}
		}
	})
}

func TestGeometricNeighborEqualCellSymmetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rects := []Rect{{W: 100, H: 100}, {X: 110, W: 100, H: 100}, {X: 220, W: 100, H: 100}}
		for i := 0; i < len(rects)-1; i++ {
			j := GeometricNeighbor(rects, i, 1, 0)
			if j != i+1 || GeometricNeighbor(rects, j, -1, 0) != i {
				t.Fatal("equal-cell neighbors not symmetric")
			}
		}

	})
}
