package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/stretchr/testify/mock"
)

// dispatchLayout is a composed layout double (newMocklayout) with the neutral
// traits of the scroll layout. A test sets the expectations of the methods it
// drives straight on it and asserts what the Workspace does with the answers,
// not that a call happened.
func dispatchLayout(t *testing.T, cols int) (*Workspace, *layoutmocks.MockLayout) {
	return dispatchLayoutWith(t, cols, scrollTraits)
}

func dispatchLayoutWith(t *testing.T, cols int, tr dispatchTraits) (*Workspace, *layoutmocks.MockLayout) {
	t.Helper()
	l := layoutmocks.NewMockLayout(t)
	l.EXPECT().NewState().Return(nil).Maybe()
	l.EXPECT().OverridesWidth().Return(true).Maybe()
	l.EXPECT().Name().Return(OverflowScroll).Maybe()
	l.EXPECT().Axes().Return(tr.content, tr.workspace).Maybe()
	l.EXPECT().EqualCells().Return(false).Maybe()
	l.EXPECT().MaximizeHides().Return(false).Maybe()
	l.EXPECT().ResetsMaximize().Return(false).Maybe()
	l.EXPECT().Scrolls().Return(tr.scrolls).Maybe()
	l.EXPECT().Spiral().Return(tr.spiral).Maybe()
	l.EXPECT().Bands().Return(false).Maybe()
	l.EXPECT().PinsFullscreen().Return(false).Maybe()
	l.EXPECT().Isolate(mock.Anything, mock.Anything).Return(false).Maybe()
	l.EXPECT().LaneRow(mock.Anything).Return(tr.laneRow).Maybe()
	w := &Workspace{Lanes: layout.Lanes{Override: l, MaxColumns: 3, Usable: Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}}
	for i := 0; i < cols; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	return w, l
}

func TestWorkspaceNavigationDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 3)
		scrollStubs(l)

		// Built-in rightward from column 0 would be 1: the layout sends focus to 2.
		l.EXPECT().FocusStep(mock.Anything, 1).Return(2).Once()
		if !w.FocusColumn(1) || w.Focus != 2 {
			t.Fatalf("focus %d, want the layout's column 2", w.Focus)
		}
		// -1 hands focus to the neighbor monitor: the workspace keeps it.
		l.EXPECT().FocusStep(mock.Anything, 1).Return(-1).Once()
		w.FocusColumn(1)
		if w.Focus != 2 {
			t.Fatalf("focus moved to %d on -1", w.Focus)
		}
		// Past the column vertically: the layout picks the column, first window.
		l.EXPECT().LaneStep(mock.Anything, -1).Return(0).Once()
		if !w.FocusWindow(-1) || w.Focus != 0 {
			t.Fatalf("lane step: focus %d, want 0", w.Focus)
		}
		l.EXPECT().LaneStep(mock.Anything, 1).Return(-1).Once()
		if w.FocusWindow(1) || w.Focus != 0 {
			t.Fatalf("no lane step must leave focus at %d", w.Focus)
		}

	})
}

func TestWorkspacePlacementDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 2)
		scrollStubs(l)

		l.EXPECT().SpawnAt(mock.Anything).Return(0, layout.InsertDefault).Once()
		w.AddWindow(9)
		if got := columnOrder(w); len(got) != 3 || got[0] != 9 || w.Focus != 0 {
			t.Fatalf("columns %v focus %d: the new window belongs at the layout's index 0", got, w.Focus)
		}
		// A covering fullscreen window keeps the view: the covered index applies.
		w.SetFullscreen(9, true)
		l.EXPECT().CoveredSpawnAt(mock.Anything).Return(1).Once()
		w.AddWindow(11)
		if got := columnOrder(w); got[1] != 11 || w.Focus != 0 {
			t.Fatalf("columns %v focus %d: covered spawn ignored", got, w.Focus)
		}
		w.SetFullscreen(9, false)
		l.EXPECT().Full(mock.Anything).Return(true).Once()
		if !w.full() {
			t.Fatal("full ignored")
		}
		// A refused swap leaves the order alone.
		w.Focus = 0
		before := columnOrder(w)
		l.EXPECT().MoveAllowed(mock.Anything, 0, 1).Return(false).Once()
		w.MoveColumn(1)
		if got := columnOrder(w); got[0] != before[0] || w.Focus != 0 {
			t.Fatalf("refused move changed %v to %v", before, got)
		}
		l.EXPECT().MoveAllowed(mock.Anything, 0, 1).Return(true).Once()
		w.MoveColumn(1)
		if got := columnOrder(w); got[0] != before[1] || w.Focus != 1 {
			t.Fatalf("allowed move: %v focus %d", got, w.Focus)
		}

	})
}

func TestWorkspaceSizingDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 2)
		scrollStubs(l)

		l.EXPECT().Grow(mock.Anything, true).Run(func(w layout.Edit, _ bool) { w.Columns[w.Focus].Cells = 3 }).Once()
		w.GrowColumn(true)
		if w.Columns[0].Cells != 3 {
			t.Fatalf("cells %d: the layout's grow was not applied to the workspace", w.Columns[0].Cells)
		}
		l.EXPECT().Shrink(mock.Anything).Run(func(w layout.Edit) { w.Columns[w.Focus].Cells-- }).Once()
		w.ShrinkColumn()
		if w.Columns[0].Cells != 2 {
			t.Fatalf("cells %d after shrink", w.Columns[0].Cells)
		}
		l.EXPECT().ToggleFullWidth(mock.Anything).Run(func(w layout.Edit) { w.Columns[w.Focus].FullWidth = true }).Once()
		w.ToggleFullWidth()
		if !w.Columns[0].FullWidth {
			t.Fatal("toggle ignored")
		}

	})
}

func TestWorkspaceGeometryDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 2)

		want := []Rect{{X: 17, Y: 23, W: 101, H: 203}, {X: 120, Y: 23, W: 50, H: 203}}
		l.EXPECT().ColumnRects(mock.Anything, []Rect(nil), layout.RectOpts{IgnoreFullWidth: true, Shift: 9}).Return(want).Once()
		got := w.columnRectsAt(nil, true, 9)
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("rects %v, want the layout's %v", got, want)
		}
		l.EXPECT().ScrollTarget(mock.Anything).Return(77).Once()
		w.scroll()
		if w.View != 77 {
			t.Fatalf("view %d, want the layout's scroll target", w.View)
		}
		l.EXPECT().ColumnWidth(mock.Anything, 1, false).Return(321).Once()
		if got := w.columnWidth(1); got != 321 {
			t.Fatalf("width %d", got)
		}
		l.EXPECT().SnapPoints(mock.Anything, mock.Anything).Return([]float64{0, 5}).Once()
		if got := w.Engine().SnapPoints(w.Snapshot(), nil); len(got) != 2 || got[1] != 5 {
			t.Fatalf("snap points %v", got)
		}
		l.EXPECT().SnapFocus(mock.Anything, 40, true).Return(1).Once()
		if got := w.snapFocus(40, true); got != 1 {
			t.Fatalf("snap focus %d", got)
		}

	})
}

func TestWorkspaceLaneDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 3)

		// Columns 0-1 form lane 0, column 2 lane 1: a move from 1 to 2 leaves the band.
		l.EXPECT().LaneOf(mock.Anything, 1).Return(0).Once()
		l.EXPECT().LaneOf(mock.Anything, 0).Return(0).Once()
		w.Focus = 0
		if w.LeavesLane(1) {
			t.Fatal("same lane must not leave the band")
		}
		l.EXPECT().LaneOf(mock.Anything, 2).Return(1).Once()
		l.EXPECT().LaneOf(mock.Anything, 0).Return(0).Once()
		if !w.LeavesLane(2) {
			t.Fatal("another lane must leave the band")
		}
		l.EXPECT().LaneBounds(mock.Anything, 1).Return(2, 2).Once()
		if first, last := w.LaneBounds(1); first != 2 || last != 2 {
			t.Fatalf("bounds %d..%d", first, last)
		}
		l.EXPECT().LaneNeighbor(mock.Anything, 1).Return(2).Once()
		if got := w.LaneNeighbor(1); got != 2 {
			t.Fatalf("neighbor %d", got)
		}

	})
}

func TestWorkspaceOverviewDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 1)
		want := []Placement{{ID: 1, Rect: Rect{X: 3, Y: 4, W: 5, H: 6}, Focused: true}}
		sel := Rect{X: 3, Y: 4, W: 5, H: 6}
		l.EXPECT().PreviewTiles(mock.Anything, mock.Anything, mock.Anything).Return(want, 77, sel).Once()
		tiles, span, got := w.previewTiles()
		if len(tiles) != 1 || tiles[0].ID != 1 || span != 77 || got != sel {
			t.Fatalf("tiles %v span %d sel %v", tiles, span, got)
		}

	})
}

// The shape traits decide how the Workspace sizes and navigates: a spiral
// layout past its row sizes every column as the spiral's slot, and one
// within its row (or without a spiral) does not.
func TestWorkspaceShapeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		spiral, row   bool
		wantInSpiral  bool
		wantSlotWidth bool
		checkWidth    bool // within its row a spiral layout sizes by lane cells
	}{
		{"spiral past the row", true, false, true, true, true},
		{"spiral within the row", true, true, false, false, false},
		{"no spiral", false, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, l := dispatchLayoutWith(t, 5, dispatchTraits{scrolls: true, content: layout.Horizontal, workspace: layout.Vertical, spiral: tc.spiral, laneRow: tc.row})
				w.Columns[0].Cells = 2 // two cells outside the spiral, one slot in it
				l.EXPECT().LaneOf(mock.Anything, mock.Anything).Return(0).Maybe()
				l.EXPECT().LaneBounds(mock.Anything, mock.Anything).Return(0, 4).Maybe()
				if got := w.InSpiral(); got != tc.wantInSpiral {
					t.Fatalf("inSpiral = %v, want %v", got, tc.wantInSpiral)
				}
				slot := w.EqualShare(max(w.MaxColumns, 1))
				if got := w.columnWidthWith(0, false, w.widthFacts()); tc.checkWidth && (got == slot) != tc.wantSlotWidth {
					t.Fatalf("width %d, spiral slot %d: slot width = %v, want %v", got, slot, got == slot, tc.wantSlotWidth)
				}

			})
		})
	}
}

// Each dispatch captures the current window, including the row within a stack,
// rather than caching the previous operation's focus or using the column head.
func TestWorkspaceEditCapturesFocusAtDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 2)
		scrollStubs(l)
		w.Columns[0].Windows = []WindowID{1, 7}
		l.EXPECT().Grow(mock.Anything, true).RunAndReturn(func(e layout.Edit, _ bool) {
			id, _ := w.Focused()
			if e.Focused != id || e.Focused != 7 || e.Lanes != &w.Lanes {
				t.Fatalf("grow edit = %+v, focus = %d", e, id)
			}
			e.Columns[e.Focus].Cells = 2
		}).Once()
		w.Columns[0].Focus = 1
		w.GrowColumn(true)
		l.EXPECT().Shrink(mock.Anything).RunAndReturn(func(e layout.Edit) {
			id, _ := w.Focused()
			if e.Focused != id || e.Focused != 2 || e.Lanes != &w.Lanes {
				t.Fatalf("shrink edit = %+v, focus = %d", e, id)
			}
		}).Once()
		w.Focus = 1
		w.ShrinkColumn()
	})
}

// Guards belong to Workspace, before dispatch and before scroll. Toggle keeps
// its original weaker guard: fullscreen prevents sizing, not maximization.
func TestWorkspaceSizingGuardsBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"empty", "float", "stash", "fullscreen"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, l := dispatchLayout(t, 1)
				switch mode {
				case "empty":
					w.Columns = nil
				case "float":
					w.Floats = []Float{{ID: 9}}
					w.floatFocus = true
				case "stash":
					w.Stash = []Float{{ID: 9}}
					w.stashFocus = true
				case "fullscreen":
					w.Fullscreen = 1
				}
				w.GrowColumn(true)
				w.ShrinkColumn()
				if mode == "fullscreen" {
					scrollStubs(l)
					l.EXPECT().ToggleFullWidth(mock.Anything).RunAndReturn(func(e layout.Edit) { e.Maximize(e.Focus) }).Once()
				}
				w.ToggleFullWidth()
			})
		})
	}
}

type dispatchTraits struct {
	scrolls, spiral, laneRow bool
	content, workspace       layout.Axis
}

var scrollTraits = dispatchTraits{scrolls: true, content: layout.Horizontal, workspace: layout.Vertical}

func scrollStubs(l *layoutmocks.MockLayout) {
	l.EXPECT().ScrollTarget(mock.Anything).Return(0).Maybe()
	l.EXPECT().ColumnWidth(mock.Anything, mock.Anything, mock.Anything).Return(100).Maybe()
}

func TestPreviewRowLanesDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, lanes := range []bool{false, true} {
			w, l := dispatchLayoutWith(t, 2, dispatchTraits{scrolls: true, content: layout.Vertical, workspace: layout.Horizontal})
			w.reserved = Rect{W: 900, H: 600}
			l.EXPECT().PreviewLanes().Return(lanes)
			l.EXPECT().PreviewTiles(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
				func(layout.View, []Placement, []Rect) ([]Placement, int, Rect) {
					return []Placement{
						{ID: 1, Rect: Rect{X: 0, Y: 0, W: 400, H: 500}, Focused: true},
						{ID: 2, Rect: Rect{X: 0, Y: 600, W: 400, H: 500}},
					}, 900, Rect{X: 0, Y: 0, W: 400, H: 500}
				})
			tiles := w.previewRow(layout.Horizontal, Rect{W: 900, H: 600}, false, true)
			if len(tiles) != 2 || tiles[0].Peek {
				t.Fatalf("lanes = %v: tiles %+v", lanes, tiles)
			}
			if tiles[1].Peek != lanes || (lanes && tiles[1].Hidden) {
				t.Fatalf("lanes = %v: second tile %+v, want peeking in its own lane = %v", lanes, tiles[1], lanes)
			}
		}

	})
}
