package core

import (
	"reflect"
	"testing"
	"testing/synctest"
)

// The overview blocks the grow and resize binds, like the other window
// mutations, and its cascade preview keeps the columns' cells.
func TestOverviewKeepsCells(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitor("", "")
		m.SetOutput(900, 600)
		m.SetOverflow(OverflowCascade)
		m.SetMaxColumns(4)
		for id := WindowID(1); id <= 2; id++ {
			m.AddWindow(id)
		}
		w := m.Current()
		w.Columns[0].Cells, w.Columns[1].Cells = 1, 3
		screen := modelColumnRects(w)
		if screen[0].W != 225 || screen[1].W != 675 {
			t.Fatalf("screen widths %v, want 225 and 675", screen)
		}
		m.ToggleOverview()
		tiles, _, _ := modelPreviewTiles(w)
		for i, p := range tiles {
			if want := screen[i]; p.Rect.W != want.W || p.Rect.X != want.X-w.Usable.X || p.Rect.H != want.H {
				t.Fatalf("column %d: preview %+v, screen %+v", i, p.Rect, want)
			}
		}
		for _, a := range []Action{ActionCycleColumnWidth, "set-column-width +1", "set-column-width -1"} {
			m.Apply(a)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{1, 3}) {
				t.Fatalf("%s in the overview: cells %v", a, got)
			}
		}

	})
}
