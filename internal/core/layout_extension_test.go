package core

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/mock"
)

// The layouts below are defined in this one file. Each embeds the neutral
// columnsBase, overrides a few methods and joins the conformance table through
// testLayouts; the interfaces, the Workspace and layoutTable are untouched.
// That shows the hooks are enough for these overrides, not that every
// possible layout fits.
//
// centeredLayout overrides one geometry method.
//
// Its columns are the scroll row, centered while they fit in the usable area.
type centeredLayout struct{ columnsBase }

const overflowCentered Overflow = "centered"

func init() { testLayouts = append(testLayouts, centeredLayout{}) }

func (centeredLayout) name() Overflow { return overflowCentered }

func (l centeredLayout) columnRects(v layoutView, dst []Rect, o rectOpts) []Rect {
	rects := l.columnsBase.columnRects(v, dst, o)
	if len(rects) == 0 {
		return rects
	}
	// The row spans first.X-gap .. last end+gap; the usable area minus that is
	// the free room, split on both sides.
	g := v.Gap
	row := rects[len(rects)-1].X + rects[len(rects)-1].W - rects[0].X + 2*g
	if free := v.Usable.W - row; free > 0 {
		for i := range rects {
			rects[i].X += free / 2
		}
	}
	return rects
}

func centeredWorkspace(n int) *Workspace {
	w := &Workspace{Overflow: overflowCentered, lay: centeredLayout{}, MaxColumns: 4,
		Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}
	for i := 0; i < n; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}, Cells: 1})
	}
	w.scroll()
	return w
}

func TestCenteredLayoutCentersFittingColumns(t *testing.T) {
	w := centeredWorkspace(2) // two cells of four: half the room is free
	rects := w.columnRectsAt(nil, false, 0)
	left := rects[0].X - w.Usable.X
	right := w.Usable.X + w.Usable.W - (rects[1].X + rects[1].W)
	if left != right || left <= w.gap() {
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
	base := columnsBase{}.columnRects(full.lview(), nil, rectOpts{})
	if got := full.columnRectsAt(nil, false, 0); !slices.Equal(got, base) {
		t.Fatalf("full row %v, want the scroll geometry %v", got, base)
	}
}

func TestCenteredLayoutInheritsNeutralBehavior(t *testing.T) {
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
}

// ladderLayout overrides only the width of a column: the base rect, scroll
// and preview methods must follow it, as they call it through the layout.
type ladderLayout struct{ columnsBase }

const overflowLadder Overflow = "ladder"

func init() { testLayouts = append(testLayouts, ladderLayout{}) }

func (ladderLayout) name() Overflow { return overflowLadder }

func (ladderLayout) overridesWidth() bool { return true }

func (ladderLayout) columnWidth(_ layoutView, i int, _ bool) int { return 100 + 50*i }

func TestColumnWidthOverrideIsHonored(t *testing.T) {
	w := &Workspace{Overflow: overflowLadder, lay: ladderLayout{}, MaxColumns: 4,
		Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}
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
	if xs, ws := w.columnEdges(); ws[3] != 250 || xs[3] != w.Usable.X+8+(100+8)+(150+8)+(200+8) {
		t.Fatalf("edges %v %v", xs, ws)
	}
}

// trailLayout has a state of its own and one trait override: it pins
// fullscreen. Its state is the mock a test sets in st; nil (the zero value,
// used by the conformance table) makes it stateless.
type trailLayout struct {
	columnsBase
	st layoutState
}

const overflowTrail Overflow = "trail"

func init() { testLayouts = append(testLayouts, trailLayout{}) }

func (trailLayout) name() Overflow          { return overflowTrail }
func (trailLayout) pinsFullscreen() bool    { return true }
func (l trailLayout) newState() layoutState { return l.st }

type trailCarry uint64

func trailWorkspace(n int, st layoutState) *Workspace {
	w := &Workspace{Overflow: overflowTrail, lay: trailLayout{st: st}, MaxColumns: 3,
		Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}
	for i := 0; i < n; i++ {
		w.AddWindow(WindowID(i + 1))
	}
	return w
}

// The hooks run for a layout outside the three: its state sees inserts and
// removals, and a carry it hands out reaches the state of the next workspace
// of that layout, while a stateless workspace in between passes it on.
func TestTrailLayoutStateAndCarry(t *testing.T) {
	// sa tags the columns it hands out; sb records the carries it receives.
	sa, sb := newMocklayoutState(t), newMocklayoutState(t)
	sa.EXPECT().inserted(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ *Workspace, _ int, _ insertKind, carry any) any { return carry }).Times(3)
	sa.EXPECT().removed(mock.Anything, mock.Anything).
		RunAndReturn(func(_ int, col Column) any { return trailCarry(col.key) }).Once()
	sa.EXPECT().settle(mock.Anything).Return().Maybe()
	var received []any
	sb.EXPECT().inserted(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ *Workspace, _ int, _ insertKind, carry any) any {
			if _, ok := carry.(trailCarry); ok {
				received = append(received, carry)
				return nil
			}
			return carry
		}).Once()
	sb.EXPECT().settle(mock.Anything).Return().Maybe()

	a, b := trailWorkspace(3, sa), trailWorkspace(0, sb)
	a.Focus = 1
	col, ok := a.takeColumn()
	if !ok {
		t.Fatal("take: no column")
	}
	if _, ok := col.carry.(trailCarry); !ok {
		t.Fatalf("carry %v, want the state's tag", col.carry)
	}
	via := Workspace{Overflow: OverflowScroll, MaxColumns: 3, Usable: a.Usable}
	via.receive(col, -1)
	col, _ = via.takeColumn()
	b.receive(col, -1)
	if len(received) != 1 || b.Columns[0].carry != nil {
		t.Fatalf("received %v, stored carry %v", received, b.Columns[0].carry)
	}
}

// Its trait override is honored by the generic fullscreen code.
func TestTrailLayoutPinsFullscreen(t *testing.T) {
	w := trailWorkspace(3, nil)
	id, _ := w.Focused()
	w.SetFullscreen(id, true)
	if !w.pinned() {
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
}
