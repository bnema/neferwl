package workspace

import (
	"reflect"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// Geometry invariants: one rect per column, widths that agree with the rects,
// no overlap inside a lane, a scroll target that settles, sorted snap points
// that map to columns, the focused column shown, and read-only methods.
func TestLayoutGeometryConformance(t *testing.T) {
	forEachConformance(t, func(t *testing.T, l layout.Layout, w *Workspace, k, n int, mode string) {
		v := w.Snapshot()
		before := slices.Clone(w.Columns)
		for _, ignore := range []bool{false, true} {
			rects := l.ColumnRects(v, nil, layout.RectOpts{IgnoreFullWidth: ignore})
			if len(rects) != n {
				t.Fatalf("%d rects for %d columns", len(rects), n)
			}
			for i, r := range rects {
				// A spiral past its row subdivides a rect without changing columnWidth;
				// bands keep their own geometry under a fullscreen column.
				inSpiral := l.Spiral() && !l.LaneRow(v)
				if width := l.ColumnWidth(v, i, ignore); !inSpiral && !(l.Bands() && mode == "fullscreen") && width != r.W {
					t.Fatalf("column %d width %d != rect %d", i, width, r.W)
				}
				if mode == "normal" || ignore {
					for j := 0; j < i; j++ {
						if l.LaneOf(v, i) == l.LaneOf(v, j) && r.Overlaps(rects[j]) {
							t.Fatalf("columns %d/%d overlap", i, j)
						}
					}
				}
			}
		}
		target := l.ScrollTarget(v)
		settled := v
		settled.View = target
		if again := l.ScrollTarget(settled); again != target {
			t.Fatalf("target not idempotent: %d -> %d", target, again)
		}
		points := l.SnapPoints(v, nil)
		if !slices.IsSorted(points) {
			t.Fatalf("unsorted points: %v", points)
		}
		for _, point := range points {
			for _, forward := range []bool{false, true} {
				if i := l.SnapFocus(v, int(point), forward); n > 0 && (i < 0 || i >= n) {
					t.Fatalf("snap focus %d outside %d columns", i, n)
				}
			}
		}
		if n > 0 {
			// Scroll columns and cascade bands intentionally extend offscreen. Only
			// the settled focused rect must fit; fullscreen scroll geometry retains
			// the usable height while its width comes from Output.
			r := l.ColumnRects(settled, nil, layout.RectOpts{})[v.Focus]
			area := v.Usable
			if mode == "fullscreen" && !l.PinsFullscreen() {
				area = v.Output
			}
			if mode != "fullscreen" && r.W <= area.W-2*v.Gap && (r.X < area.X || r.X+r.W > area.X+area.W) ||
				r.Y < area.Y || r.Y+r.H > area.Y+area.H {
				t.Fatalf("focused rect %+v not shown in %+v", r, area)
			}
			if l.Spiral() && mode == "normal" {
				for _, r := range l.ColumnRects(v, nil, layout.RectOpts{}) {
					if r.X < area.X || r.Y < area.Y || r.X+r.W > area.X+area.W || r.Y+r.H > area.Y+area.H {
						t.Fatalf("fixed rect %+v outside %+v", r, area)
					}
				}
			}
		}
		if !slices.EqualFunc(before, w.Columns, func(a, b Column) bool { return reflect.DeepEqual(a, b) }) {
			t.Fatal("read methods mutated columns")
		}
	})
}

// scrollTarget runs on every settle: production layouts must not allocate.
// Generated extension mocks record calls and allocate by design.
func TestScrollTargetAllocations(t *testing.T) {
	for _, l := range registeredLayouts() {
		t.Run(string(l.Name()), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := conformanceWorkspace(l, 2, 5, "normal")
				w.Focus = 4
				v := w.Snapshot()
				if allocs := testing.AllocsPerRun(100, func() { _ = l.ScrollTarget(v) }); allocs != 0 {
					t.Fatalf("scrollTarget allocates %g times", allocs)
				}

			})
		})
	}
}
