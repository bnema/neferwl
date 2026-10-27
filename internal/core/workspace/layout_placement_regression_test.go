package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// A stateless workspace builds no state for what a cascade column carries: the
// carry stays on the stored column, and the next takeColumn passes it on, so
// the neighbors come back when the column returns to cascade.

// A layout switch drops the state, and the carry parked on a column with it.

// A fullscreen window the layout does not pin is previewed in its column at
// the usable width, not the output's the live geometry gives it.

func TestLayoutSwitchDropsParkedCarry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := Workspace{Lanes: layout.Lanes{Overflow: OverflowScroll, MaxColumns: 3, Usable: Rect{W: 900, H: 600},
			Columns: []Column{{Windows: []WindowID{1}, Carry: new(int)}}}}
		w.ResetLayoutState()
		if w.Columns[0].Carry != nil {
			t.Fatal("carry survived the layout switch")
		}

	})
}

func TestScrollPreviewFullscreenColumnUsesUsableWidth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := conformanceWorkspace(mustLayout(OverflowScroll), 3, 3, "fullscreen")
		if w.Output.W == w.Usable.W {
			t.Fatal("fixture needs distinct output and usable widths")
		}
		if w.Pinned() {
			t.Fatal("scroll must not pin")
		}
		tiles, span, sel := w.previewTilesInto(nil)
		want := w.Usable.W - 2*w.Gap()
		if sel.W != want {
			t.Fatalf("selection width %d, want the usable %d (output %d)", sel.W, want, w.Output.W)
		}
		for _, p := range tiles {
			if p.Focused && p.Rect.W != want {
				t.Fatalf("focused tile width %d, want %d", p.Rect.W, want)
			}
		}
		if span < want {
			t.Fatalf("span %d narrower than the column %d", span, want)
		}

	})
}

func TestCascadeExplicitPlacementUnderIntermediateFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 6, 2, false)
		w.FocusID(3)
		w.SetFullscreen(3, true)
		w.addColumn(Column{Windows: []WindowID{7}})
		if i, j := w.ColumnOf(7), w.ColumnOf(3); i != j+1 {
			t.Fatalf("new column at %d, fullscreen at %d", i, j)
		}
		if id, _ := w.Focused(); id != 3 {
			t.Fatalf("focus %d", id)
		}

	})
}

func TestCascadeScrollTargetDoesNotMutateState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 2, 2, false)
		v := w.Snapshot()
		v.MaxColumns = 1
		w.Engine().ScrollTarget(v)
		if w.BandStart(1) {
			t.Fatal("snapshot scrollTarget changed owner's boundary")
		}

	})
}

func TestCascadeCoveredSpawnGoesLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 4, 2, false)
		w.FocusID(2)
		w.SetFullscreen(2, true)
		v := w.Snapshot()
		if at := w.Engine().CoveredSpawnAt(v); at != len(w.Columns) {
			t.Fatalf("covered spawn %d, want %d", at, len(w.Columns))
		}
		w.AddWindow(9)
		if got := columnOrder(w); got[len(got)-1] != 9 {
			t.Fatalf("columns %v: the covered window belongs last", got)
		}
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("focus %d", id)
		}

	})
}

func TestCascadePreviewMaximizedColumnFillsItsBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cells := range []int{0, 1, 2, 5} {
			for _, span := range []int{0, 4000} {
				_, w := cascadeMovesCore(t, 4, 3, false)
				w.FocusID(2)
				w.Columns[w.ColumnOf(2)].Cells = cells
				w.ToggleFullWidth()
				if first, last := w.LaneBounds(w.LaneOf(w.ColumnOf(2))); first != last {
					t.Fatalf("maximized column shares its band %d..%d", first, last)
				}
				w.Columns[w.ColumnOf(2)].Span = span
				_, _, sel := w.previewTilesInto(nil)
				if want := w.Usable.W - 2*w.Gap(); sel.X != w.Gap() || sel.W != want {
					t.Fatalf("cells %d span %d: selection %+v, want X %d W %d", cells, span, sel, w.Gap(), want)
				}
			}
		}

	})
}

func TestCascadeColumnTransferRestoresNeighbors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 3, 3, false)
		w.FocusID(2)
		w.ToggleFullWidth()
		dst := Workspace{Lanes: layout.Lanes{Overflow: OverflowCascade, MaxColumns: 3, Usable: w.Usable}}
		col, ok := w.takeColumn()
		if !ok {
			t.Fatal("take")
		}
		oldKey := col.Key
		if col.Carry == nil {
			t.Fatal("missing transfer payload")
		}
		// Exercise a destination identity collision using a real local column.
		dst.Columns = []Column{{Key: oldKey}}
		dst.SettleLayout()
		dst.Columns = nil
		dst.receive(col, -1)
		received := dst.Columns[dst.ColumnOf(2)]
		if received.Key == oldKey {
			t.Fatal("destination reused source identity")
		}
		if received.Carry != nil {
			t.Fatal("destination retained consumed transfer payload")
		}
		col, ok = dst.takeColumn()
		if !ok {
			t.Fatal("return")
		}
		w.receive(col, 1)
		if w.Columns[w.ColumnOf(2)].Carry != nil {
			t.Fatal("return retained consumed transfer payload")
		}
		w.ToggleFullWidth()
		wantBands(t, w, []WindowID{1, 2, 3})

	})
}

func TestCarryHopsThroughStatelessWorkspace(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, w := cascadeMovesCore(t, 3, 3, false)
				w.FocusID(2)
				w.ToggleFullWidth()
				if n := w.Engine().LaneCount(w.Snapshot()); n != 3 {
					t.Fatalf("lanes %d after maximizing, want 3", n)
				}
				col, ok := w.takeColumn()
				if !ok || col.Carry == nil {
					t.Fatalf("take: ok %v, carry %v", ok, col.Carry)
				}
				mid := Workspace{Lanes: layout.Lanes{Overflow: o, MaxColumns: 3, Usable: w.Usable}}
				mid.receive(col, -1)
				if mid.Snapshot().State != nil {
					t.Fatalf("%s workspace built state %T", o, mid.Snapshot().State)
				}
				col, ok = mid.takeColumn()
				if !ok || col.Carry == nil {
					t.Fatalf("hop: ok %v, carry %v", ok, col.Carry)
				}
				w.receive(col, 1)
				if w.Columns[w.ColumnOf(2)].Carry != nil {
					t.Fatal("carry not consumed")
				}
				w.ToggleFullWidth()
				wantBands(t, w, []WindowID{1, 2, 3})
				if n := w.Engine().LaneCount(w.Snapshot()); n != 1 {
					t.Fatalf("lanes %d after restoring, want 1", n)
				}

			})
		})
	}
}
