package core

import (
	"testing"
	"testing/synctest"
)

func TestCascadeColumnTransferRestoresNeighbors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 3, 3, false)
		w.FocusID(2)
		w.ToggleFullWidth()
		dst := Workspace{Overflow: OverflowCascade, MaxColumns: 3, Usable: w.Usable}
		col, ok := w.takeColumn()
		if !ok {
			t.Fatal("take")
		}
		oldKey := col.key
		if col.carry == nil {
			t.Fatal("missing transfer payload")
		}
		if _, exists := w.cascadeState().cols[oldKey]; exists {
			t.Fatal("source retained transferred state")
		}
		dst.colSeq = oldKey // Exercise a destination identity collision.
		dst.receive(col, -1)
		received := dst.Columns[dst.columnOf(2)]
		if received.key == oldKey {
			t.Fatal("destination reused source identity")
		}
		if received.carry != nil {
			t.Fatal("destination retained consumed transfer payload")
		}
		if state := dst.cascadeState().cols[received.key]; state == nil || len(state.pushedUp)+len(state.pushedDown) == 0 {
			t.Fatal("destination lost remembered neighbors")
		}
		col, ok = dst.takeColumn()
		if !ok {
			t.Fatal("return")
		}
		w.receive(col, 1)
		if w.Columns[w.columnOf(2)].carry != nil {
			t.Fatal("return retained consumed transfer payload")
		}
		w.ToggleFullWidth()
		wantBands(t, w, []WindowID{1, 2, 3})

	})
}

// A stateless workspace builds no state for what a cascade column carries: the
// carry stays on the stored column, and the next takeColumn passes it on, so
// the neighbors come back when the column returns to cascade.
func TestCarryHopsThroughStatelessWorkspace(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, w := cascadeMovesCore(t, 3, 3, false)
				w.FocusID(2)
				w.ToggleFullWidth()
				if n := w.layout().laneCount(w.lview()); n != 3 {
					t.Fatalf("lanes %d after maximizing, want 3", n)
				}
				col, ok := w.takeColumn()
				if !ok || col.carry == nil {
					t.Fatalf("take: ok %v, carry %v", ok, col.carry)
				}
				mid := Workspace{Overflow: o, MaxColumns: 3, Usable: w.Usable}
				mid.receive(col, -1)
				if mid.lstate != nil {
					t.Fatalf("%s workspace built state %T", o, mid.lstate)
				}
				col, ok = mid.takeColumn()
				if !ok || col.carry == nil {
					t.Fatalf("hop: ok %v, carry %v", ok, col.carry)
				}
				w.receive(col, 1)
				if w.Columns[w.columnOf(2)].carry != nil {
					t.Fatal("carry not consumed")
				}
				w.ToggleFullWidth()
				wantBands(t, w, []WindowID{1, 2, 3})
				if n := w.layout().laneCount(w.lview()); n != 1 {
					t.Fatalf("lanes %d after restoring, want 1", n)
				}

			})
		})
	}
}

// A layout switch drops the state, and the carry parked on a column with it.
func TestLayoutSwitchDropsParkedCarry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := Workspace{Overflow: OverflowScroll, MaxColumns: 3, Usable: Rect{W: 900, H: 600},
			Columns: []Column{{Windows: []WindowID{1}, carry: &cascadeCol{}}}}
		w.resetLayoutState()
		if w.Columns[0].carry != nil {
			t.Fatal("carry survived the layout switch")
		}

	})
}

func TestCascadeExplicitPlacementUnderIntermediateFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 6, 2, false)
		w.FocusID(3)
		w.SetFullscreen(3, true)
		w.addColumn(Column{Windows: []WindowID{7}})
		if i, j := w.columnOf(7), w.columnOf(3); i != j+1 {
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
		v := w.lview()
		v.MaxColumns = 1
		w.layout().scrollTarget(v)
		if w.bandStart(1) {
			t.Fatal("snapshot scrollTarget changed owner's boundary")
		}

	})
}

// A window arriving under a covering fullscreen window waits at the end of a
// cascade workspace, not after the focused column as in scroll.
func TestCascadeCoveredSpawnGoesLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w := cascadeMovesCore(t, 4, 2, false)
		w.FocusID(2)
		w.SetFullscreen(2, true)
		v := w.lview()
		if at := w.layout().coveredSpawnAt(v); at != len(w.Columns) {
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

// A fullscreen window the layout does not pin is previewed in its column at
// the usable width, not the output's the live geometry gives it.
func TestScrollPreviewFullscreenColumnUsesUsableWidth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := conformanceWorkspace(scrollLayout{}, 3, 3, "fullscreen")
		if w.Output.W == w.Usable.W {
			t.Fatal("fixture needs distinct output and usable widths")
		}
		if w.pinned() {
			t.Fatal("scroll must not pin")
		}
		tiles, span, sel := w.previewTilesInto(nil)
		want := w.Usable.W - 2*w.gap()
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

// Invariant: a maximized cascade column fills its band, whatever its cells or
// manual width. It always forms a band of its own, so the band walk gives its
// overview tile the full usable width from the left gap.
func TestCascadePreviewMaximizedColumnFillsItsBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cells := range []int{0, 1, 2, 5} {
			for _, span := range []int{0, 4000} {
				_, w := cascadeMovesCore(t, 4, 3, false)
				w.FocusID(2)
				w.Columns[w.columnOf(2)].Cells = cells
				w.ToggleFullWidth()
				if first, last := w.laneBounds(w.laneOf(w.columnOf(2))); first != last {
					t.Fatalf("maximized column shares its band %d..%d", first, last)
				}
				w.Columns[w.columnOf(2)].Span = span
				_, _, sel := w.previewTilesInto(nil)
				if want := w.Usable.W - 2*w.gap(); sel.X != w.gap() || sel.W != want {
					t.Fatalf("cells %d span %d: selection %+v, want X %d W %d", cells, span, sel, w.gap(), want)
				}
			}
		}

	})
}

// A workspace made from the monitor template must not share its layout state
// with the template or with another workspace: the cascade bands of one would
// leak into the other. The template builds state when SetMaxColumns settles
// it, and every workspace made afterwards used to alias that state.
func TestNewWorkspaceDoesNotShareLayoutState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(1920, 1080)
		m.SetUsable(Rect{W: 1920, H: 1080})
		m.SetGaps(8)
		m.SetOverflow(OverflowCascade)
		m.SetMaxColumns(2)
		m.AddWindow(1)
		m.Apply(ActionFocusWorkspaceNext) // a second workspace, made from the template
		m.AddWindow(2)
		m.AddWindow(3)
		m.AddWindow(4)
		// The column leaves for a third workspace, made from the template again.
		m.Apply(ActionMoveColumnToWorkspaceNext)

		w := m.Workspaces[m.Active]
		if len(w.Columns) != 2 {
			t.Fatalf("columns = %d, want 2", len(w.Columns))
		}
		// Two columns fit the two-column capacity: one band, side by side.
		r := w.columnRectsAt(nil, false, 0)
		if r[0].Y != r[1].Y || r[0].X == r[1].X {
			t.Fatalf("columns are not in one band: %v", r)
		}
		for i, a := range m.Workspaces {
			for j, b := range m.Workspaces {
				if i < j && a.lstate != nil && a.lstate == b.lstate {
					t.Fatalf("workspaces %d and %d share layout state", i, j)
				}
			}
			if a.lstate != nil && a.lstate == m.template.lstate {
				t.Fatalf("workspace %d shares the template's layout state", i)
			}
		}

	})
}
