package workspace

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/stretchr/testify/mock"
)

// Extension layouts use the generated composed mock, delegating unchanged
// behavior to the real neutral base so the conformance coverage stays intact.
const (
	overflowCentered Overflow = "centered"
	overflowLadder   Overflow = "ladder"
	overflowTrail    Overflow = "trail"
)

func init() {
	for _, name := range []Overflow{overflowCentered, overflowLadder, overflowTrail} {
		testLayouts = append(testLayouts, extensionLayout(name, nil))
	}
}

func extensionLayout(name Overflow, st layout.State) *layoutmocks.MockLayout {
	return extensionLayoutWith(name, func() layout.State { return st })
}

// extensionLayoutWith is extensionLayout with newState answering each NewState call.
func extensionLayoutWith(name Overflow, newState func() layout.State) *layoutmocks.MockLayout {
	l := &layoutmocks.MockLayout{}
	base := layout.Base{}
	l.EXPECT().Name().Return(name)
	l.EXPECT().NewState().RunAndReturn(newState)
	l.EXPECT().PinsFullscreen().Return(name == overflowTrail)
	l.EXPECT().OverridesWidth().Return(name == overflowLadder)
	l.EXPECT().ColumnWidth(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(v layout.View, i int, ignore bool) int {
		if name == overflowLadder {
			return 100 + 50*i
		}
		return base.ColumnWidth(v, i, ignore)
	})
	l.EXPECT().ColumnRects(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(v layout.View, dst []Rect, o layout.RectOpts) []Rect {
		rects := base.ColumnRects(v, dst, o)
		if name == overflowCentered && len(rects) > 0 {
			row := rects[len(rects)-1].X + rects[len(rects)-1].W - rects[0].X + 2*v.Gap
			if free := v.Usable.W - row; free > 0 {
				for i := range rects {
					rects[i].X += free / 2
				}
			}
		}
		return rects
	})
	l.EXPECT().Axes().RunAndReturn(base.Axes)
	l.EXPECT().EqualCells().RunAndReturn(base.EqualCells)
	l.EXPECT().MaximizeHides().RunAndReturn(base.MaximizeHides)
	l.EXPECT().Scrolls().RunAndReturn(base.Scrolls)
	l.EXPECT().ResetsMaximize().RunAndReturn(base.ResetsMaximize)
	l.EXPECT().Bands().RunAndReturn(base.Bands)
	l.EXPECT().LaneGestures().RunAndReturn(base.LaneGestures)
	l.EXPECT().Spiral().RunAndReturn(base.Spiral)
	l.EXPECT().ScrollTarget(mock.Anything).RunAndReturn(base.ScrollTarget)
	l.EXPECT().SnapPoints(mock.Anything, mock.Anything).RunAndReturn(base.SnapPoints)
	l.EXPECT().SnapFocus(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(base.SnapFocus)
	l.EXPECT().LaneOf(mock.Anything, mock.Anything).RunAndReturn(base.LaneOf)
	l.EXPECT().LaneBounds(mock.Anything, mock.Anything).RunAndReturn(base.LaneBounds)
	l.EXPECT().LaneCount(mock.Anything).RunAndReturn(base.LaneCount)
	l.EXPECT().LaneNeighbor(mock.Anything, mock.Anything).RunAndReturn(base.LaneNeighbor)
	l.EXPECT().LaneRow(mock.Anything).RunAndReturn(base.LaneRow)
	l.EXPECT().FocusStep(mock.Anything, mock.Anything).RunAndReturn(base.FocusStep)
	l.EXPECT().LaneStep(mock.Anything, mock.Anything).RunAndReturn(base.LaneStep)
	l.EXPECT().SpawnAt(mock.Anything).RunAndReturn(base.SpawnAt)
	l.EXPECT().CoveredSpawnAt(mock.Anything).RunAndReturn(base.CoveredSpawnAt)
	l.EXPECT().Full(mock.Anything).RunAndReturn(base.Full)
	l.EXPECT().MoveAllowed(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(base.MoveAllowed)
	l.EXPECT().MoveLane(mock.Anything, mock.Anything).RunAndReturn(base.MoveLane)
	l.EXPECT().Isolate(mock.Anything, mock.Anything).RunAndReturn(base.Isolate)
	l.EXPECT().AdoptFloat(mock.Anything, mock.Anything).RunAndReturn(base.AdoptFloat)
	l.EXPECT().PreviewTiles(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(base.PreviewTiles)
	l.EXPECT().PreviewLanes().RunAndReturn(base.PreviewLanes)
	l.EXPECT().Grow(mock.Anything, mock.Anything).RunAndReturn(base.Grow)
	l.EXPECT().Shrink(mock.Anything).RunAndReturn(base.Shrink)
	l.EXPECT().ToggleFullWidth(mock.Anything).RunAndReturn(base.ToggleFullWidth)
	return l
}

func centeredWorkspace(n int) *Workspace {
	w := &Workspace{Lanes: layout.Lanes{Overflow: overflowCentered, Override: extensionLayout(overflowCentered, nil), MaxColumns: 4,
		Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}}
	for i := 0; i < n; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}, Cells: 1})
	}
	w.scroll()
	return w
}

func TestCenteredLayoutCentersFittingColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := centeredWorkspace(2) // two cells of four: half the room is free
		rects := w.columnRectsAt(nil, false, 0)
		left := rects[0].X - w.Usable.X
		right := w.Usable.X + w.Usable.W - (rects[1].X + rects[1].W)
		if left != right || left <= w.Gap() {
			t.Fatalf("margins %d / %d: the row is not centered (rects %v)", left, right, rects)
		}
		// The windows the workspace publishes follow the layout's rects.
		for i, p := range w.Layout() {
			if p.Rect.X != rects[i].X {
				t.Fatalf("window %d at x=%d, layout says %d", p.ID, p.Rect.X, rects[i].X)
			}
		}
		// A full row has no room to center: the scroll geometry is unchanged.
		full := centeredWorkspace(4)
		base := layout.Base{}.ColumnRects(full.Snapshot(), nil, layout.RectOpts{})
		if got := full.columnRectsAt(nil, false, 0); !slices.Equal(got, base) {
			t.Fatalf("full row %v, want the scroll geometry %v", got, base)
		}

	})
}

func TestCenteredLayoutInheritsNeutralBehavior(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := centeredWorkspace(3)
		w.Focus = 0
		w.FocusColumn(1)
		if w.Focus != 1 {
			t.Fatalf("focus %d after focus right", w.Focus)
		}
		w.AddWindow(9)
		if w.Columns[w.Focus].Windows[0] != 9 || w.Focus != 2 {
			t.Fatalf("new window at column %d: scroll places it after the focus", w.Focus)
		}
		w.MoveColumn(-1)
		if w.Columns[1].Windows[0] != 9 {
			t.Fatalf("move left: %v", columnOrder(w))
		}

	})
}

func TestColumnWidthOverrideIsHonored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &Workspace{Lanes: layout.Lanes{Overflow: overflowLadder, Override: extensionLayout(overflowLadder, nil), MaxColumns: 4,
			Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}}
		for i := 0; i < 4; i++ {
			w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
		}
		w.scroll()
		rects := w.columnRectsAt(nil, false, 0)
		for i, r := range rects {
			if r.W != 100+50*i {
				t.Fatalf("rect %d width %d, want the layout's %d", i, r.W, 100+50*i)
			}
		}
		tiles, _, _ := w.previewTilesInto(nil)
		for i, p := range tiles {
			if p.Rect.W != 100+50*i {
				t.Fatalf("preview tile %d width %d, want the layout's %d", i, p.Rect.W, 100+50*i)
			}
		}
		// The edges and the scroll target follow too: the last column is shown.
		w.Focus = 3
		w.scroll()
		if r := w.columnRectsAt(nil, false, 0)[3]; r.X < w.Usable.X || r.X+r.W > w.Usable.X+w.Usable.W {
			t.Fatalf("focused rect %+v not scrolled into %+v", r, w.Usable)
		}
		if xs, ws := w.ColumnEdges(); ws[3] != 250 || xs[3] != w.Usable.X+8+(100+8)+(150+8)+(200+8) {
			t.Fatalf("edges %v %v", xs, ws)
		}

	})
}

type trailCarry uint64

func trailWorkspace(n int, st layout.State) *Workspace {
	w := &Workspace{Lanes: layout.Lanes{Overflow: overflowTrail, Override: extensionLayout(overflowTrail, st), MaxColumns: 3,
		Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}}
	for i := 0; i < n; i++ {
		w.AddWindow(WindowID(i + 1))
	}
	return w
}

// The hooks run for a layout outside the three: its state sees inserts and
// removals, and a carry it hands out reaches the state of the next workspace
// of that layout, while a stateless workspace in between passes it on.
func TestTrailLayoutStateAndCarry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// sa tags the columns it hands out; sb records the carries it receives.
		sa, sb := layoutmocks.NewMockState(t), layoutmocks.NewMockState(t)
		sa.EXPECT().Inserted(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			RunAndReturn(func(_ *layout.Lanes, _ int, _ layout.InsertKind, carry any) any { return carry }).Times(3)
		sa.EXPECT().Removed(mock.Anything, mock.Anything).
			RunAndReturn(func(_ int, col Column) any { return trailCarry(col.Key) }).Once()
		sa.EXPECT().Settle(mock.Anything).Return().Maybe()
		var received []any
		sb.EXPECT().Inserted(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			RunAndReturn(func(_ *layout.Lanes, _ int, _ layout.InsertKind, carry any) any {
				if _, ok := carry.(trailCarry); ok {
					received = append(received, carry)
					return nil
				}
				return carry
			}).Once()
		sb.EXPECT().Settle(mock.Anything).Return().Maybe()

		a, b := trailWorkspace(3, sa), trailWorkspace(0, sb)
		a.Focus = 1
		col, ok := a.takeColumn()
		if !ok {
			t.Fatal("take: no column")
		}
		if _, ok := col.Carry.(trailCarry); !ok {
			t.Fatalf("carry %v, want the state's tag", col.Carry)
		}
		via := Workspace{Lanes: layout.Lanes{Overflow: OverflowScroll, MaxColumns: 3, Usable: a.Usable}}
		via.receive(col, -1)
		col, _ = via.takeColumn()
		b.receive(col, -1)
		if len(received) != 1 || b.Columns[0].Carry != nil {
			t.Fatalf("received %v, stored carry %v", received, b.Columns[0].Carry)
		}

	})
}

// Its trait override is honored by the generic fullscreen code.
func TestTrailLayoutPinsFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := trailWorkspace(3, nil)
		id, _ := w.Focused()
		w.SetFullscreen(id, true)
		if !w.Pinned() {
			t.Fatal("fullscreen is not pinned in a layout that pins it")
		}
		// A window other than the focused one may not take the cover, as in every
		// layout that pins; scroll lets it.
		other := WindowID(1)
		if id == other {
			other = 2
		}
		if w.mayCover(other) {
			t.Fatal("an unfocused window must not cover in a pinning layout")
		}

	})
}
