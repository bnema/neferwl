package layout

import (
	"slices"
	"testing"
	"testing/synctest"
)

func TestSnapPointsReusableMatchesLegacy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, n := range []int{1, 2, 16, 64} {
			for _, stamped := range []bool{false, true} {
				for _, width := range []int{0, 1, 900, 1920} {
					w := benchLane(n, stamped)
					w.Usable.W = width
					xs, widths := w.ColumnEdges()
					g := w.Gap()
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
						got := scrollSnapPoints(w.Snapshot(), dst)
						if !slices.Equal(got, want) {
							t.Fatalf("n=%d stamped=%t width=%d: got %v want %v", n, stamped, width, got, want)
						}
					}
				}
			}
		}

	})
}

func benchLane(n int, stamped bool) *Lanes {
	w := &Lanes{Overflow: OverflowScroll, MaxColumns: 3, Output: Rect{W: 1000, H: 800}, Usable: Rect{W: 1000, H: 800}, Gaps: 10}
	for i := 0; i < n; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	if stamped {
		stamp := w.NewStamp()
		for i := range w.Columns {
			w.Columns[i].Span = 10000 / n
			w.Columns[i].SpanStamp = stamp
		}
		w.Columns[n-1].Span += 10000 % n
	}
	w.Focus = n / 2
	w.SettleLayout()
	return w
}
