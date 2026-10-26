package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/mock"
)

func TestSnapPointsReusableMatchesLegacy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, n := range []int{1, 2, 16, 64} {
			for _, stamped := range []bool{false, true} {
				for _, width := range []int{0, 1, 900, 1920} {
					w := benchLane(n, stamped)
					w.Usable.W = width
					xs, widths := w.columnEdges()
					g := w.gap()
					lo := xs[0] - (w.Usable.X + g)
					hi := max(xs[n-1]+widths[n-1]-(w.Usable.X+width-g), lo)
					var views []int
					for i := range xs {
						views = append(views, min(max(xs[i]-(w.Usable.X+g), lo), hi), min(max(xs[i]+widths[i]-(w.Usable.X+width-g), lo), hi))
					}
					slices.Sort(views)
					step := float64(width) * snapSpacing
					want := []float64{float64(lo)}
					for _, x := range views {
						if float64(x)-want[len(want)-1] >= step {
							want = append(want, float64(x))
						}
					}
					if float64(hi) != want[len(want)-1] {
						if len(want) > 1 && float64(hi)-want[len(want)-1] < step {
							want = want[:len(want)-1]
						}
						want = append(want, float64(hi))
					}
					for _, dst := range [][]float64{nil, make([]float64, 0, 2*n+2)} {
						got := scrollSnapPoints(w.lview(), dst)
						if !slices.Equal(got, want) {
							t.Fatalf("n=%d stamped=%t width=%d: got %v want %v", n, stamped, width, got, want)
						}
					}
				}
			}
		}

	})
}

func TestStatelessLayoutInitialization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
			w := Workspace{lanes: lanes{Overflow: o}}
			if w.state() != nil || !w.lstateReady {
				t.Fatal("stateless layout not initialized")
			}
			w.resetLayoutState()
			if w.lstateReady {
				t.Fatal("state initialization not reset")
			}
			w.lview()
			if !w.lstateReady {
				t.Fatal("snapshot did not initialize state")
			}
		}

	})
}

func TestStatelessLayoutBuildsStateOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 3)
		// dispatchLayout allows newState at will; this test counts it.
		l.Mock.ExpectedCalls = slices.DeleteFunc(l.Mock.ExpectedCalls, func(c *mock.Call) bool { return c.Method == "newState" })
		l.EXPECT().newState().Return(nil).Once()
		for range 3 {
			w.state()
			w.lview()
		}
		l.AssertNumberOfCalls(t, "newState", 1)

		w.resetLayoutState()
		l.EXPECT().newState().Return(nil).Once()
		for range 3 {
			w.lview()
		}
		l.AssertNumberOfCalls(t, "newState", 2)

	})
}
