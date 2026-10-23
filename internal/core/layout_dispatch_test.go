package core

import (
	"testing"

	"github.com/stretchr/testify/mock"
)

// dispatchTraits are the constant answers of a layout double.
type dispatchTraits struct {
	scrolls, spiral, laneRow bool
	content, workspace       layoutAxis
}

// scrollTraits are the neutral scroll layout's.
var scrollTraits = dispatchTraits{scrolls: true, content: horizontalAxis, workspace: verticalAxis}

// dispatchLayout is a composed layout double (newMocklayout) with the neutral
// traits of the scroll layout. A test sets the expectations of the methods it
// drives straight on it and asserts what the Workspace does with the answers,
// not that a call happened.
func dispatchLayout(t *testing.T, cols int) (*Workspace, *mocklayout) {
	return dispatchLayoutWith(t, cols, scrollTraits)
}

func dispatchLayoutWith(t *testing.T, cols int, tr dispatchTraits) (*Workspace, *mocklayout) {
	t.Helper()
	l := newMocklayout(t)
	l.EXPECT().newState().Return(nil).Maybe()
	l.EXPECT().overridesWidth().Return(true).Maybe()
	l.EXPECT().name().Return(OverflowScroll).Maybe()
	l.EXPECT().axes().Return(tr.content, tr.workspace).Maybe()
	l.EXPECT().equalCells().Return(false).Maybe()
	l.EXPECT().maximizeHides().Return(false).Maybe()
	l.EXPECT().resetsMaximize().Return(false).Maybe()
	l.EXPECT().scrolls().Return(tr.scrolls).Maybe()
	l.EXPECT().spiral().Return(tr.spiral).Maybe()
	l.EXPECT().bands().Return(false).Maybe()
	l.EXPECT().pinsFullscreen().Return(false).Maybe()
	l.EXPECT().isolate(mock.Anything, mock.Anything).Return(false).Maybe()
	l.EXPECT().laneRow(mock.Anything).Return(tr.laneRow).Maybe()
	w := &Workspace{lay: l, MaxColumns: 3, Usable: Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}
	for i := 0; i < cols; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	return w, l
}

// scrollStubs answers the geometry a scroll() settle asks for.
func scrollStubs(l *mocklayout) {
	l.EXPECT().scrollTarget(mock.Anything).Return(0).Maybe()
	l.EXPECT().columnWidth(mock.Anything, mock.Anything, mock.Anything).Return(100).Maybe()
}

func TestWorkspaceNavigationDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 3)
	scrollStubs(l)

	// Built-in rightward from column 0 would be 1: the layout sends focus to 2.
	l.EXPECT().focusStep(mock.Anything, 1).Return(2).Once()
	if !w.FocusColumn(1) || w.Focus != 2 {
		t.Fatalf("focus %d, want the layout's column 2", w.Focus)
	}
	// -1 hands focus to the neighbor monitor: the workspace keeps it.
	l.EXPECT().focusStep(mock.Anything, 1).Return(-1).Once()
	w.FocusColumn(1)
	if w.Focus != 2 {
		t.Fatalf("focus moved to %d on -1", w.Focus)
	}
	// Past the column vertically: the layout picks the column, first window.
	l.EXPECT().laneStep(mock.Anything, -1).Return(0).Once()
	if !w.FocusWindow(-1) || w.Focus != 0 {
		t.Fatalf("lane step: focus %d, want 0", w.Focus)
	}
	l.EXPECT().laneStep(mock.Anything, 1).Return(-1).Once()
	if w.FocusWindow(1) || w.Focus != 0 {
		t.Fatalf("no lane step must leave focus at %d", w.Focus)
	}
}

func TestWorkspacePlacementDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 2)
	scrollStubs(l)

	l.EXPECT().spawnAt(mock.Anything).Return(0, insertDefault).Once()
	w.AddWindow(9)
	if got := columnOrder(w); len(got) != 3 || got[0] != 9 || w.Focus != 0 {
		t.Fatalf("columns %v focus %d: the new window belongs at the layout's index 0", got, w.Focus)
	}
	// A covering fullscreen window keeps the view: the covered index applies.
	w.SetFullscreen(9, true)
	l.EXPECT().coveredSpawnAt(mock.Anything).Return(1).Once()
	w.AddWindow(11)
	if got := columnOrder(w); got[1] != 11 || w.Focus != 0 {
		t.Fatalf("columns %v focus %d: covered spawn ignored", got, w.Focus)
	}
	w.SetFullscreen(9, false)
	l.EXPECT().full(mock.Anything).Return(true).Once()
	if !w.full() {
		t.Fatal("full ignored")
	}
	// A refused swap leaves the order alone.
	w.Focus = 0
	before := columnOrder(w)
	l.EXPECT().moveAllowed(mock.Anything, 0, 1).Return(false).Once()
	w.MoveColumn(1)
	if got := columnOrder(w); got[0] != before[0] || w.Focus != 0 {
		t.Fatalf("refused move changed %v to %v", before, got)
	}
	l.EXPECT().moveAllowed(mock.Anything, 0, 1).Return(true).Once()
	w.MoveColumn(1)
	if got := columnOrder(w); got[0] != before[1] || w.Focus != 1 {
		t.Fatalf("allowed move: %v focus %d", got, w.Focus)
	}
}

func TestWorkspaceSizingDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 2)
	scrollStubs(l)

	l.EXPECT().grow(w, true).Run(func(w *Workspace, _ bool) { w.Columns[w.Focus].Cells = 3 }).Once()
	w.GrowColumn(true)
	if w.Columns[0].Cells != 3 {
		t.Fatalf("cells %d: the layout's grow was not applied to the workspace", w.Columns[0].Cells)
	}
	l.EXPECT().shrink(w).Run(func(w *Workspace) { w.Columns[w.Focus].Cells-- }).Once()
	w.ShrinkColumn()
	if w.Columns[0].Cells != 2 {
		t.Fatalf("cells %d after shrink", w.Columns[0].Cells)
	}
	l.EXPECT().toggleFullWidth(w).Run(func(w *Workspace) { w.Columns[w.Focus].FullWidth = true }).Once()
	w.ToggleFullWidth()
	if !w.Columns[0].FullWidth {
		t.Fatal("toggle ignored")
	}
}

func TestWorkspaceGeometryDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 2)

	want := []Rect{{X: 17, Y: 23, W: 101, H: 203}, {X: 120, Y: 23, W: 50, H: 203}}
	l.EXPECT().columnRects(mock.Anything, []Rect(nil), rectOpts{IgnoreFullWidth: true, Shift: 9}).Return(want).Once()
	got := w.columnRectsAt(nil, true, 9)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rects %v, want the layout's %v", got, want)
	}
	l.EXPECT().scrollTarget(mock.Anything).Return(77).Once()
	w.scroll()
	if w.View != 77 {
		t.Fatalf("view %d, want the layout's scroll target", w.View)
	}
	l.EXPECT().columnWidth(mock.Anything, 1, false).Return(321).Once()
	if got := w.columnWidth(1); got != 321 {
		t.Fatalf("width %d", got)
	}
	l.EXPECT().snapPoints(mock.Anything, mock.Anything).Return([]float64{0, 5}).Once()
	if got := w.snapPoints(); len(got) != 2 || got[1] != 5 {
		t.Fatalf("snap points %v", got)
	}
	l.EXPECT().snapFocus(mock.Anything, 40, true).Return(1).Once()
	if got := w.snapFocus(40, true); got != 1 {
		t.Fatalf("snap focus %d", got)
	}
}

func TestWorkspaceLaneDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 3)

	// Columns 0-1 form lane 0, column 2 lane 1: a move from 1 to 2 leaves the band.
	l.EXPECT().laneOf(mock.Anything, 1).Return(0).Once()
	l.EXPECT().laneOf(mock.Anything, 0).Return(0).Once()
	w.Focus = 0
	if w.leavesLane(1) {
		t.Fatal("same lane must not leave the band")
	}
	l.EXPECT().laneOf(mock.Anything, 2).Return(1).Once()
	l.EXPECT().laneOf(mock.Anything, 0).Return(0).Once()
	if !w.leavesLane(2) {
		t.Fatal("another lane must leave the band")
	}
	l.EXPECT().laneBounds(mock.Anything, 1).Return(2, 2).Once()
	if first, last := w.laneBounds(1); first != 2 || last != 2 {
		t.Fatalf("bounds %d..%d", first, last)
	}
	l.EXPECT().laneNeighbor(mock.Anything, 1).Return(2).Once()
	if got := w.laneNeighbor(1); got != 2 {
		t.Fatalf("neighbor %d", got)
	}
}

// The position hint counts a workspace's rows from the layout's lane count.
func TestPositionHintLaneCountDispatch(t *testing.T) {
	_, l := dispatchLayoutWith(t, 0, dispatchTraits{scrolls: true, content: verticalAxis, workspace: horizontalAxis})
	m := newMonitor("m", "")
	w := m.Current()
	w.lay, w.Usable = l, Rect{W: 900, H: 600}
	l.EXPECT().laneCount(mock.Anything).Return(3).Once()
	l.EXPECT().laneOf(mock.Anything, 0).Return(1).Once()
	h := positionHintGrid(m)
	if h.Counts[0] != 3 || h.ActiveRow != 1 {
		t.Fatalf("hint rows %d, active row %d: want the layout's 3 lanes and lane 1", h.Counts[0], h.ActiveRow)
	}
}

// A swipe scrolls the columns only in a layout whose content axis slides.
func TestSwipeTraitDispatch(t *testing.T) {
	for _, scrolls := range []bool{true, false} {
		c := layoutCore(0, tile{"M", 0, 0, 900, 600})
		c.cfg.Animations.On = true
		sc := c.screens[0]
		w := sc.mon.Current()
		_, l := dispatchLayoutWith(t, 0, dispatchTraits{scrolls: scrolls, content: horizontalAxis, workspace: verticalAxis})
		l.EXPECT().snapPoints(mock.Anything, mock.Anything).Return([]float64{0, 400}).Maybe()
		w.lay, w.Usable = l, Rect{W: 900, H: 600}
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}}
		g := &swipeGesture{screen: sc, fingers: 3, cx: 40}
		c.decide(g)
		if got := g.mode == swipeColumns; got != scrolls {
			t.Fatalf("scrolls = %v: swipe mode %v", scrolls, g.mode)
		}
	}
}

func TestWorkspaceOverviewDispatch(t *testing.T) {
	w, l := dispatchLayout(t, 1)
	want := []Placement{{ID: 1, Rect: Rect{X: 3, Y: 4, W: 5, H: 6}, Focused: true}}
	sel := Rect{X: 3, Y: 4, W: 5, H: 6}
	l.EXPECT().previewTiles(mock.Anything, mock.Anything, mock.Anything).Return(want, 77, sel).Once()
	tiles, span, got := w.previewTiles()
	if len(tiles) != 1 || tiles[0].ID != 1 || span != 77 || got != sel {
		t.Fatalf("tiles %v span %d sel %v", tiles, span, got)
	}
}

// previewLanes makes the overview read a tile's lane from its Y: the tile one
// Usable.H down sits in another lane, dimmed, instead of in the focused one.
func TestPreviewRowLanesDispatch(t *testing.T) {
	for _, lanes := range []bool{false, true} {
		w, l := dispatchLayoutWith(t, 2, dispatchTraits{scrolls: true, content: verticalAxis, workspace: horizontalAxis})
		w.reserved = Rect{W: 900, H: 600}
		l.EXPECT().previewLanes().Return(lanes)
		l.EXPECT().previewTiles(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(layoutView, []Placement, []Rect) ([]Placement, int, Rect) {
				return []Placement{
					{ID: 1, Rect: Rect{X: 0, Y: 0, W: 400, H: 500}, Focused: true},
					{ID: 2, Rect: Rect{X: 0, Y: 600, W: 400, H: 500}},
				}, 900, Rect{X: 0, Y: 0, W: 400, H: 500}
			})
		tiles := w.previewRow(horizontalAxis, Rect{W: 900, H: 600}, false, true)
		if len(tiles) != 2 || tiles[0].Peek {
			t.Fatalf("lanes = %v: tiles %+v", lanes, tiles)
		}
		if tiles[1].Peek != lanes || (lanes && tiles[1].Hidden) {
			t.Fatalf("lanes = %v: second tile %+v, want peeking in its own lane = %v", lanes, tiles[1], lanes)
		}
	}
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
			w, l := dispatchLayoutWith(t, 5, dispatchTraits{scrolls: true, content: horizontalAxis, workspace: verticalAxis, spiral: tc.spiral, laneRow: tc.row})
			w.Columns[0].Cells = 2 // two cells outside the spiral, one slot in it
			l.EXPECT().laneOf(mock.Anything, mock.Anything).Return(0).Maybe()
			l.EXPECT().laneBounds(mock.Anything, mock.Anything).Return(0, 4).Maybe()
			if got := w.inSpiral(); got != tc.wantInSpiral {
				t.Fatalf("inSpiral = %v, want %v", got, tc.wantInSpiral)
			}
			slot := w.equalShare(max(w.MaxColumns, 1))
			if got := w.columnWidthWith(0, false, w.widthFacts()); tc.checkWidth && (got == slot) != tc.wantSlotWidth {
				t.Fatalf("width %d, spiral slot %d: slot width = %v, want %v", got, slot, got == slot, tc.wantSlotWidth)
			}
		})
	}
}
