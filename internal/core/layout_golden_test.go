package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

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
			synctest.Test(t, func(t *testing.T) {
				w := Workspace{Lanes: layout.Lanes{Overflow: tc.o, MaxColumns: 2, Usable: Rect{W: 1000, H: 720}, Output: Rect{W: 1000, H: 720}, Gaps: 8}}
				for i := 0; i < 5; i++ {
					w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
				}
				modelScroll(&w)
				g := swipeGesture{ws: &w, points: w.Engine().SnapPoints(w.Snapshot(), nil)}
				g.scratch = w.Engine().SnapPoints(w.Snapshot(), g.scratch[:0])
				if allocs := testing.AllocsPerRun(50, func() {
					g.scratch = g.ws.Engine().SnapPoints(g.ws.Snapshot(), g.scratch[:0])
					if !slices.Equal(g.scratch, g.points) {
						t.Fatal("points moved")
					}
				}); allocs != tc.allocs {
					t.Fatalf("staleness check allocates %g times, want %g", allocs, tc.allocs)
				}

			})
		})
	}
}
