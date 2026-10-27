package layout

import (
	"reflect"
	"testing"
	"testing/synctest"
)

// Columns of a lane share the usable width by their cells; unset columns
// share what the others leave.
func TestLaneWidths(t *testing.T) {
	cells := func(w *Lanes, c ...int) []int {
		for i, n := range c {
			w.Columns[i].Cells = n
		}
		var got []int
		for i := range w.Columns {
			v := w.Snapshot()
			got = append(got, v.cellWidth(i))
		}
		return got
	}
	for _, overflow := range []Overflow{OverflowCascade, OverflowFixed} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mk := func(n int) *Lanes {
					w := &Lanes{MaxColumns: 4, Overflow: overflow, Gaps: 8, Usable: Rect{W: 1000, H: 500}, Output: Rect{W: 1000, H: 500}}
					for id := 1; id <= n; id++ {
						w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(id)}})
						w.ColumnInserted(len(w.Columns)-1, InsertDefault)
					}
					return w
				}
				avail := 1000 - 3*8
				if got, want := cells(mk(2), 1, 3), []int{avail / 4, avail * 3 / 4}; !reflect.DeepEqual(got, want) {
					t.Errorf("1,3: got %v want %v", got, want)
				}
				// Auto columns share what the set ones leave.
				w := mk(3)
				avail = 1000 - 4*8
				if got, want := cells(w, 2, 0, 0), []int{avail * 2 / 4, avail / 4, avail / 4}; !reflect.DeepEqual(got, want) {
					t.Errorf("2,auto,auto: got %v want %v", got, want)
				}
				// No room for them: an auto column weighs one cell.
				if got, want := cells(w, 3, 0, 0), []int{avail * 3 / 5, avail / 5, avail - avail*3/5 - avail/5}; !reflect.DeepEqual(got, want) {
					t.Errorf("3,auto,auto: got %v want %v", got, want)
				}
				// All auto: equal shares, with remaining pixels in the last column.
				for n := 1; n <= 4; n++ {
					w := mk(n)
					for i := range w.Columns {
						want := w.EqualShare(n)
						if i == n-1 {
							want = 1000 - 8*(n+1) - (n-1)*want
						}
						v := w.Snapshot()
						if got := v.cellWidth(i); got != want {
							t.Errorf("%d auto columns: width %d want %d", n, got, want)
						}
					}
				}

			})
		})
	}
}
