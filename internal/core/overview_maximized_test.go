package core

import (
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
	m.ToggleOverview()
	if got := w.stack(); !slices.Equal(got, []stackItem{{stackColumn, 2}, {stackColumn, 1}, {stackColumn, 3}, {stackColumn, 4}}) {
		t.Fatalf("stack %v", got)
	}
	front := previewOf(t, m.Layout(), 2)
	if front.Peek || !front.Focused {
		t.Fatalf("front %+v", front)
	}
	for _, id := range []WindowID{1, 3} {
		if p := previewOf(t, m.Layout(), id); !p.Peek || p.Hidden {
			t.Fatalf("peek %d %+v", id, p)
		}
	}
	if p := previewOf(t, m.Layout(), 4); !p.Hidden {
		t.Fatalf("fourth should be hidden %+v", p)
	}
	m.OverviewMove(0, -1)
	if m.stackFront(w) != (stackItem{stackColumn, 1}) || !previewOf(t, m.Layout(), 1).Focused {
		t.Fatal("up did not select first hidden column")
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 1 || !w.Columns[0].FullWidth || w.Columns[1].FullWidth {
		t.Fatalf("transfer focus %d cols %+v", id, w.Columns)
	}
	m.ToggleOverview()
	if got := w.stack()[1]; got != (stackItem{stackColumn, 2}) {
		t.Fatalf("MRU behind: %v", got)
	}
	m.CancelOverview()
}

func TestOverviewMaximizedCancelAndClick(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.AddFloating(9, 600, 400)
	w.FocusID(2)
	before := slices.Clone(order(w))
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	m.OverviewMove(0, -1)
	m.CancelOverview()
	if id, _ := w.Focused(); id != 2 || !w.Columns[1].FullWidth || !slices.Equal(order(w), before) {
		t.Fatalf("cancel focus %d order %v", id, order(w))
	}
	m.ToggleOverview()
	p := previewOf(t, m.Layout(), 3)
	if !p.Peek {
		t.Fatalf("peek %+v", p)
	}
	m.OverviewPick(3)
	if id, _ := w.Focused(); id != 3 || !w.Columns[2].FullWidth {
		t.Fatalf("click focus %d max %+v", id, w.Columns)
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
	if got := w.stack(); !slices.Equal(got, []stackItem{{stackFloat, 9}, {stackColumn, 2}, {stackColumn, 1}, {stackColumn, 3}, {stackColumn, 4}}) {
		t.Fatalf("stack %v", got)
	}
	if p, q := previewOf(t, m.Layout(), 10), previewOf(t, m.Layout(), 11); !p.Peek || !q.Peek || p.Focused || q.Focused {
		t.Fatalf("neighbor %+v %+v", p, q)
	}
	m.OverviewMove(0, -1)
	if m.stackFront(w) != (stackItem{stackColumn, 2}) {
		t.Fatalf("front %v", m.stackFront(w))
	}
	m.OverviewMove(0, 1)
	if m.stackFront(w) != (stackItem{stackFloat, 9}) {
		t.Fatalf("back %v", m.stackFront(w))
	}
	m.CancelOverview()
}

func TestOverviewMaximizedLeftAndStash(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.FocusID(4)
	w.ToggleWindowStash()
	w.FocusID(2)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.cardAt(w) >= 0 || m.stackFront(w) != (stackItem{stackColumn, 1}) {
		t.Fatalf("left should send card behind: %v", m.stackFront(w))
	}
	m.OverviewMove(0, 1)
	if m.stackFront(w) != (stackItem{stackColumn, 2}) {
		t.Fatalf("front %v", m.stackFront(w))
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

// In scroll overflow, Escape leaves a maximized column the focus left.
func TestOverviewCancelKeepsScrollFullWidth(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.Overflow = OverflowScroll
	w.FocusID(3) // column 2 stays maximized in scroll overflow
	if !w.Columns[1].FullWidth {
		t.Fatal("setup: column 2 not maximized")
	}
	m.ToggleOverview()
	m.CancelOverview()
	if !w.Columns[1].FullWidth {
		t.Fatalf("escape cleared column 2: %+v", w.Columns)
	}
}

// A selected single-column card follows its anchor when earlier columns go.
func TestOverviewHiddenSelectionFollowsWindowAfterRemoval(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	m.OverviewMove(0, -1) // column 3
	w.RemoveWindow(1)
	if p := previewOf(t, m.Layout(), 3); !p.Focused || p.Peek {
		t.Fatalf("selection moved: %+v", p)
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 3 || !w.Columns[1].FullWidth {
		t.Fatalf("confirm %d %+v", id, w.Columns)
	}
}

func TestOverviewHiddenSelectionRemovedFallsBack(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	m.OverviewMove(0, -1)
	w.RemoveWindow(3)
	if p := previewOf(t, m.Layout(), 4); !p.Focused {
		t.Fatalf("fallback %+v", p)
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 4 {
		t.Fatalf("fallback %d", id)
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
	m.OverviewMove(0, -1)
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
	if got := w.stack(); !slices.Equal(got, []stackItem{{kind: stackColumns}}) {
		t.Fatalf("scroll items %v", got)
	}
	for _, id := range []WindowID{1, 3, 4} {
		if p := previewOf(t, m.Layout(), id); p.Hidden || p.Peek {
			t.Fatalf("scroll column %d %+v", id, p)
		}
	}
}
