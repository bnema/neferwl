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
	w.Columns[2].Windows = append(w.Columns[2].Windows, 5)
	rects := w.hiddenColumnRects()
	m.ToggleOverview()
	if got := w.stack(); !slices.Equal(got, []stackItem{{stackColumn, 2}, {kind: stackColumns}}) {
		t.Fatalf("items %v", got)
	}
	if p := previewOf(t, m.Layout(), 2); p.Hidden || p.Peek || !p.Focused {
		t.Fatalf("maximized front %+v", p)
	}
	ps := m.Layout()
	for _, id := range []WindowID{1, 3, 4, 5} {
		p := previewOf(t, ps, id)
		i := w.columnOf(id)
		r := rects[i]
		if id == 3 || id == 5 {
			r = stackRects(r, 2, w.gap())[map[WindowID]int{3: 0, 5: 1}[id]]
		}
		if p.Hidden || !p.Peek || p.Focused || math.Abs(float64(p.Rect.W)-float64(r.W)*p.Preview) > 1 || math.Abs(float64(p.Rect.H)-float64(r.H)*p.Preview) > 1 {
			t.Fatalf("hidden tile %d: %+v cell %+v", id, p, r)
		}
		for _, other := range []WindowID{1, 3, 4, 5} {
			if id < other && p.Rect.Overlaps(previewOf(t, ps, other).Rect) {
				t.Fatalf("hidden tiles %d/%d overlap", id, other)
			}
		}
	}
	// The hidden columns fill the card as if the maximized column were
	// gone: no hole where it was.
	area := 0
	for _, id := range []WindowID{1, 3, 4, 5} {
		r := previewOf(t, ps, id).Rect
		area += r.W * r.H
	}
	z := previewOf(t, ps, 1).Preview
	full := float64(w.Usable.W) * float64(w.Usable.H) * z * z
	if float64(area) < 0.8*full {
		t.Fatalf("hidden tiles cover %d of %.0f: hole left by the maximized column", area, full)
	}
	m.OverviewMove(0, -1)
	if m.stackFront(w).kind != stackColumns || m.hiddenColumn(w) != 0 || !previewOf(t, m.Layout(), 1).Focused || w.Focus != 1 || !w.Columns[1].FullWidth {
		t.Fatal("up changed real maximization or failed to select hidden card")
	}
	m.OverviewMove(1, 0)
	if m.hiddenColumn(w) != 2 || !previewOf(t, m.Layout(), 3).Focused || w.Focus != 1 {
		t.Fatal("right did not select column 3 provisionally")
	}
	m.OverviewMove(-1, 0)
	if m.hiddenColumn(w) != 0 {
		t.Fatal("left did not skip maximized slot")
	}
	m.OverviewMove(1, 0)
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 3 || !w.Columns[2].FullWidth || w.Columns[1].FullWidth || !previewOf(t, w.Layout(), 1).Hidden {
		t.Fatalf("confirm focus %d columns %+v", id, w.Columns)
	}
	m.ToggleOverview()
	if m.hiddenColumn(w) != 1 || m.stackFront(w).kind != stackColumn {
		t.Fatal("previously maximized column not selected behind front")
	}
	m.OverviewMove(0, -1)
	if p := previewOf(t, m.Layout(), 2); !p.Focused || p.Peek {
		t.Fatalf("MRU selection %+v", p)
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
	if got := w.stack(); !slices.Equal(got, []stackItem{{stackFloat, 9}, {stackColumn, 2}, {kind: stackColumns}}) {
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
	if m.cardAt(w) >= 0 || m.stackFront(w).kind != stackColumns {
		t.Fatalf("left should send card behind: %v", m.stackFront(w))
	}
	m.OverviewMove(0, 1)
	if m.stackFront(w) != (stackItem{stackColumn, 2}) {
		t.Fatalf("front %v", m.stackFront(w))
	}
	m.CancelOverview()
}

// Escape restores the opening column state after a real workspace mutation.
func TestOverviewCancelRestoresFullWidthAfterMutation(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	w.ToggleFullWidth()
	if w.Columns[1].FullWidth {
		t.Fatal("workspace mutation did not toggle maximization")
	}
	m.CancelOverview()
	if w.Focus != 1 || !w.Columns[1].FullWidth {
		t.Fatalf("cancel lost maximization: focus %d columns %+v", w.Focus, w.Columns)
	}
	// The snapshot follows a surviving column if another column disappears.
	m.ToggleOverview()
	w.RemoveWindow(1)
	w.ToggleFullWidth()
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
	m.OverviewMove(1, 0) // select column 3 inside the group
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
	m.OverviewMove(1, 0) // select column 3 inside the group
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
		v, _ := s.next(p, configureTarget{output: "test", client: p.Rect, view: whole(w.Usable)})
		s.mark(v)
		before[p.ID] = v
	}
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	for _, p := range m.Layout() {
		if p.ID < 1 || p.ID > 4 {
			continue
		}
		v, _ := s.next(p, configureTarget{output: "test", view: whole(w.Usable)})
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
