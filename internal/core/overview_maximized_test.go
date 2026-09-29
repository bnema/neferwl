package core

import (
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func maximizedOverview() *Monitor {
	m := newMonitor("", "")
	m.SetOutput(600, 400)
	m.SetOverflow(OverflowFixed)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 4; id++ {
		m.AddWindow(id)
	}
	m.Current().FocusID(2)
	m.Current().ToggleFullWidth()
	return m
}

func TestOverviewMaximizedGeometryAndNavigation(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	rects := w.columnRectsFor(true)
	maxRect := previewOf(t, w.Layout(), 2).Rect
	m.ToggleOverview()
	ps := m.Layout()
	front := previewOf(t, ps, 2)
	if front.Peek || !front.Focused || front.Hidden || math.Abs(float64(front.Rect.W)-float64(maxRect.W)*front.Preview) > 1 {
		t.Fatalf("maximized front %+v", front)
	}
	for _, id := range []WindowID{1, 3, 4} {
		p := previewOf(t, ps, id)
		r := rects[int(id)-1]
		if p.Hidden || !p.Peek || p.Focused || math.Abs(float64(p.Rect.W)-float64(r.W)*p.Preview) > 1 || math.Abs(float64(p.Rect.H)-float64(r.H)*p.Preview) > 1 {
			t.Fatalf("hidden card %d: %+v slot %+v", id, p, r)
		}
		for _, other := range []WindowID{1, 3, 4} {
			if other > id && p.Rect.Overlaps(previewOf(t, ps, other).Rect) {
				t.Fatalf("hidden card overlaps %d and %d", id, other)
			}
		}
	}
	// The front wins even where a peeking tile overlaps it.
	if got := m.overviewAt(float64(front.Rect.X+front.Rect.W/2), float64(front.Rect.Y+front.Rect.H/2)); got != 2 {
		t.Fatalf("front hit %d", got)
	}
	m.OverviewMove(1, 0)
	if m.stackFront(w) != 0 || m.hiddenColumn(w) != 0 || !w.Columns[1].FullWidth || w.Focus != 1 {
		t.Fatal("rotation changed the workspace")
	}
	m.OverviewMove(1, 0)
	if m.hiddenColumn(w) != 2 || !previewOf(t, m.Layout(), 3).Focused {
		t.Fatal("right did not select column 3")
	}
	m.OverviewMove(-1, 0)
	if m.hiddenColumn(w) != 0 {
		t.Fatal("left did not skip the maximized column")
	}
	m.OverviewMove(1, 0)
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 3 || w.Columns[1].FullWidth {
		t.Fatalf("confirm focus %d max %v", id, w.Columns[1].FullWidth)
	}
	for _, id := range []WindowID{1, 2, 3, 4} {
		if previewOf(t, w.Layout(), id).Hidden {
			t.Fatalf("%d still hidden", id)
		}
	}
}

func TestOverviewMaximizedCancelAndClick(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.AddFloating(9, 600, 400)
	w.FocusID(2) // covering float below both column cards
	before := slices.Clone(order(w))
	m.ToggleOverview()
	m.OverviewMove(1, 0) // hidden card
	m.OverviewMove(1, 0) // another hidden column
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0) // wrap to covering float
	m.CancelOverview()
	if id, _ := w.Focused(); id != 2 || w.Focus != 1 || !w.Columns[1].FullWidth || !slices.Equal(order(w), before) {
		t.Fatalf("cancel focus %d column %d max %v floats %v", id, w.Focus, w.Columns[1].FullWidth, order(w))
	}
	m.ToggleOverview()
	ps := m.Layout()
	peek := previewOf(t, ps, 3)
	x, y := peek.Rect.X+peek.Rect.W-1, peek.Rect.Y+1
	if got := m.overviewAt(float64(x), float64(y)); got != 3 {
		t.Fatalf("peek hit %d", got)
	}
	m.OverviewPick(3)
	if id, _ := w.Focused(); id != 3 || w.Columns[1].FullWidth {
		t.Fatalf("peek click focus %d max %v", id, w.Columns[1].FullWidth)
	}
}

func TestOverviewMaximizedFloatWrapAndNeighbor(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.AddFloating(9, 600, 400)
	m.Focus(1)
	m.AddWindow(10)
	m.AddWindow(11)
	m.Current().FocusID(10)
	m.Current().ToggleFullWidth()
	m.Focus(0)
	m.ToggleOverview()
	if got := w.stackItems(); !slices.Equal(got, []WindowID{0, overviewMaxColumn, 9}) {
		t.Fatalf("items %v", got)
	}
	if p, q := previewOf(t, m.Layout(), 10), previewOf(t, m.Layout(), 11); !p.Peek || !q.Peek || p.Focused || q.Focused {
		t.Fatalf("neighbor max %+v hidden %+v", p, q)
	}
	m.OverviewMove(1, 0) // max
	if m.stackFront(w) != overviewMaxColumn {
		t.Fatal("max not front")
	}
	m.OverviewMove(1, 0) // hidden
	if m.stackFront(w) != 0 {
		t.Fatal("hidden not front")
	}
	m.OverviewMove(1, 0) // skip max, column 3
	m.OverviewMove(1, 0) // column 4
	m.OverviewMove(1, 0) // float
	if m.stackFront(w) != 9 {
		t.Fatalf("float not front: %d", m.stackFront(w))
	}
	m.OverviewMove(1, 0) // max
	if m.stackFront(w) != overviewMaxColumn {
		t.Fatal("wrap did not reach max")
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 2 || !w.Columns[1].FullWidth {
		t.Fatalf("confirm max focus %d max %v", id, w.Columns[1].FullWidth)
	}
}

func TestOverviewMaximizedLeftAndStash(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.stackFront(w) != 0 || w.Columns[1].FullWidth == false {
		t.Fatal("left did not rotate backwards to the hidden card")
	}
	m.CancelOverview()
	w.FocusID(4)
	w.ToggleWindowStash()
	w.FocusID(2)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.OverviewMove(-1, 0) // maximized -> hidden, not stash
	if m.cardAt(w) >= 0 || m.stackFront(w) != 0 {
		t.Fatal("maximized left skipped hidden card")
	}
	m.OverviewMove(-1, 0) // first hidden column -> stash
	if m.cardAt(w) < 0 {
		t.Fatal("hidden card did not enter stash")
	}
	m.OverviewMove(1, 0)
	if m.stackFront(w) != 0 || w.Columns[1].FullWidth == false {
		t.Fatal("stash return changed maximization")
	}
	m.CancelOverview()
}

// Escape restores the opening column state even when an active bind changes it.
func TestOverviewCancelRestoresFullWidthAfterBind(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.Apply(ActionMaximizeColumn)
	if w.Columns[1].FullWidth {
		t.Fatal("maximize bind did not toggle while overview was open")
	}
	m.CancelOverview()
	if w.Focus != 1 || !w.Columns[1].FullWidth {
		t.Fatalf("cancel lost maximization: focus %d columns %+v", w.Focus, w.Columns)
	}
	// The snapshot follows a surviving column if another column disappears.
	m.ToggleOverview()
	w.RemoveWindow(1)
	m.Apply(ActionMaximizeColumn)
	m.CancelOverview()
	if id, _ := w.Focused(); id != 2 || !w.Columns[0].FullWidth {
		t.Fatalf("cancel after removal: focus %d columns %+v", id, w.Columns)
	}
}

// Escape maximizes only the column holding the window focused at opening,
// where the focus returns, even when binds split its column meanwhile.
func TestOverviewCancelFullWidthAfterSplit(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.FocusID(3)
	w.ConsumeOrExpel(-1) // column 2 holds [2 3]
	w.FocusID(3)
	w.ToggleFullWidth()
	m.ToggleOverview()
	w.FocusID(3)
	w.ConsumeOrExpel(1) // 3 leaves for another column
	w.FocusID(2)
	w.ToggleFullWidth() // 2's column maximized instead
	m.CancelOverview()
	maxed := 0
	for _, c := range w.Columns {
		if c.FullWidth {
			maxed++
			if !slices.Contains(c.Windows, 3) {
				t.Fatalf("wrong column maximized: %+v", w.Columns)
			}
		}
	}
	if id, _ := w.Focused(); maxed != 1 || id != 3 {
		t.Fatalf("%d maximized, focus %d: %+v", maxed, id, w.Columns)
	}
}

// The selected hidden card follows its window when earlier columns disappear.
func TestOverviewHiddenSelectionFollowsWindowAfterRemoval(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(1, 0) // hidden card, selects column 1
	m.OverviewMove(1, 0) // column 3
	w.RemoveWindow(1)
	if p := previewOf(t, m.Layout(), 3); !p.Focused || p.Peek {
		t.Fatalf("selection moved after removal: %+v", p)
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 3 || w.Columns[0].FullWidth {
		t.Fatalf("confirm after removal: focus %d columns %+v", id, w.Columns)
	}
}

func TestOverviewHiddenSelectionRemovedFallsBack(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0) // selected column 3
	w.RemoveWindow(3)
	if m.hiddenColumn(w) != 2 || !previewOf(t, m.Layout(), 4).Focused {
		t.Fatal("removed selection did not fall back to nearest hidden column")
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 4 {
		t.Fatalf("fallback confirmed %d, want 4", id)
	}
}

func TestOverviewMaximizedPreviewsKeepConfigures(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	s := newConfigures()
	before := map[WindowID]ports.ConfigureWindow{}
	for _, p := range w.Layout() {
		// Hidden clients retain their last unmaximized configure.
		if p.Hidden {
			r := w.columnRectsFor(true)[int(p.ID)-1]
			p.Rect, p.Hidden = r, false
		}
		v, _ := s.next(p, configureTarget{output: "test", client: p.Rect, area: w.Usable})
		s.mark(v)
		before[p.ID] = v
	}
	m.ToggleOverview()
	m.OverviewMove(1, 0)
	for _, p := range m.Layout() {
		if p.ID < 1 || p.ID > 4 {
			continue
		}
		v, _ := s.next(p, configureTarget{output: "test", area: w.Usable})
		if v.Width != before[p.ID].Width || v.Height != before[p.ID].Height {
			t.Fatalf("%d resized from %+v to %+v", p.ID, before[p.ID], v)
		}
	}
}

func TestOverviewScrollFullWidthUnchanged(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.Overflow = OverflowScroll
	m.ToggleOverview()
	if got := w.stackItems(); !slices.Equal(got, []WindowID{0}) {
		t.Fatalf("scroll items %v", got)
	}
	for _, id := range []WindowID{1, 3, 4} {
		if p := previewOf(t, m.Layout(), id); p.Hidden || p.Peek {
			t.Fatalf("scroll column %d %+v", id, p)
		}
	}
}
