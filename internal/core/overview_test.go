package core

import (
	"math"
	"testing"
)

// overviewMonitor has workspace 1 with columns 1, 2, 3 (a third of the
// width each) on a 300x200 output, and workspace 2 with column 4.
func overviewMonitor() *Monitor {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	m.Focus(1)
	m.AddWindow(4)
	m.Focus(0)
	return m
}

func previewOf(t *testing.T, ps []Placement, id WindowID) Placement {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no placement for %d", id)
	return Placement{}
}

// Previews keep each tile's shape: width and height shrink by the same
// factor, and the row fits the usable width.
func TestOverviewPreviewsKeepAspect(t *testing.T) {
	m := overviewMonitor()
	normal := previewOf(t, m.Layout(), 2).Rect
	m.ToggleOverview()
	ps := m.Layout()
	for id := WindowID(1); id <= 3; id++ {
		p := previewOf(t, ps, id)
		if p.Hidden || p.Preview <= 0 || p.Peek {
			t.Fatalf("%d: %+v", id, p)
		}
		if p.Rect.X < 0 || p.Rect.X+p.Rect.W > 300 {
			t.Fatalf("%d off screen: %+v", id, p.Rect)
		}
	}
	p := previewOf(t, ps, 2)
	got, want := float64(p.Rect.W)/float64(p.Rect.H), float64(normal.W)/float64(normal.H)
	if math.Abs(got-want) > 0.02 {
		t.Fatalf("aspect %v, want %v (%+v)", got, want, p.Rect)
	}
	if math.Abs(float64(p.Rect.W)-float64(normal.W)*p.Preview) > 1 {
		t.Fatalf("width %d, zoom %v of %d", p.Rect.W, p.Preview, normal.W)
	}
	// Workspace 2 is below, dimmed and unfocused.
	below := previewOf(t, ps, 4)
	if below.Hidden || !below.Peek || below.Focused || below.Rect.Y <= p.Rect.Y+p.Rect.H {
		t.Fatalf("below %+v, current %+v", below, p)
	}
}

// Many columns shrink down to the floor, then the row scrolls to keep
// the selected column on screen.
func TestOverviewWideWorkspaceScrolls(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(1)
	for id := WindowID(1); id <= 10; id++ {
		m.AddWindow(id)
	}
	m.ToggleOverview()
	ps := m.Layout()
	p := previewOf(t, ps, 10)
	if p.Preview != overviewMinZoom || !p.Focused || p.Rect.X < 0 || p.Rect.X+p.Rect.W > 300 {
		t.Fatalf("%+v", p)
	}
}

// h/l and j/k move the selection; Escape returns, Return keeps it.
func TestOverviewNavigation(t *testing.T) {
	m := overviewMonitor()
	m.Current().FocusID(3)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if id, _ := m.Focused(); id != 2 {
		t.Fatalf("left: %d", id)
	}
	m.OverviewMove(0, 1)
	if id, _ := m.Focused(); id != 4 || m.Active != 1 {
		t.Fatalf("down: %d on %d", id, m.Active)
	}
	// The empty workspace below the last one is not entered.
	m.OverviewMove(0, 1)
	if m.Active != 1 {
		t.Fatalf("entered empty workspace %d", m.Active)
	}
	m.CancelOverview()
	if id, _ := m.Focused(); id != 3 || m.Active != 0 || m.overview {
		t.Fatalf("cancel: %d on %d", id, m.Active)
	}
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.ToggleOverview()
	if id, _ := m.Focused(); id != 4 || m.Active != 1 || m.overview {
		t.Fatalf("confirm: %d on %d", id, m.Active)
	}
}

// A click picks the preview under it.
func TestOverviewPick(t *testing.T) {
	m := overviewMonitor()
	m.ToggleOverview()
	below := previewOf(t, m.Layout(), 4).Rect
	id := m.overviewAt(float64(below.X+1), float64(below.Y+1))
	if id != 4 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewPick(id)
	if f, _ := m.Focused(); f != 4 || m.overview {
		t.Fatalf("pick: %d", f)
	}
}

// pileMonitor is overviewMonitor with 5, 6 and 7 stashed on workspace 1
// (7 selected) and the stash hidden.
func pileMonitor() *Monitor {
	m := overviewMonitor()
	w := m.Current()
	for id := WindowID(5); id <= 7; id++ {
		w.AddWindow(id)
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	w.ToggleStashVisible()
	w.FocusID(1)
	return m
}

// The stash shows as a pile on the left of its row, even hidden: its
// selected window in front, the others behind, dimmed. The row starts
// right of it.
func TestOverviewPileLayout(t *testing.T) {
	m := pileMonitor()
	m.ToggleOverview()
	ps := m.Layout()
	front, back := previewOf(t, ps, 7), previewOf(t, ps, 5)
	if front.Hidden || front.Peek || front.Focused || front.Preview != overviewCardZoom {
		t.Fatalf("front %+v", front)
	}
	if back.Hidden || !back.Peek || back.Rect.X <= front.Rect.X || back.Rect.Y >= front.Rect.Y {
		t.Fatalf("back %+v, front %+v", back, front)
	}
	first := previewOf(t, ps, 1)
	if !first.Focused || first.Rect.X < back.Rect.X+back.Rect.W {
		t.Fatalf("row %+v overlaps the pile %+v", first.Rect, back.Rect)
	}
	// Front last: drawn and hit on top.
	idx := func(id WindowID) int {
		for i, p := range ps {
			if p.ID == id {
				return i
			}
		}
		return -1
	}
	if idx(7) < idx(5) || idx(7) < idx(6) {
		t.Fatal("front card drawn under the others")
	}
}

// h from column 1 enters the pile on the stash's selection, h/l browse
// it, l past its end returns to column 1; Return shows the stash on the
// chosen window.
func TestOverviewPileNavigation(t *testing.T) {
	m := pileMonitor()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if at := m.pileAt(w); at != 2 {
		t.Fatalf("entered at %d", at)
	}
	if f := previewOf(t, m.Layout(), 7); !f.Focused {
		t.Fatalf("front card not selected: %+v", f)
	}
	if previewOf(t, m.Layout(), 1).Focused {
		t.Fatal("column still selected")
	}
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0) // stops at the first entry
	if at := m.pileAt(w); at != 0 {
		t.Fatalf("left end %d", at)
	}
	m.OverviewMove(1, 0)
	m.ToggleOverview()
	if f, _ := m.Focused(); f != 6 || w.stashHidden || m.overview {
		t.Fatalf("confirm on %d, hidden %v", f, w.stashHidden)
	}
	// Back in on the stash's selection (6), then l past the last entry
	// (7) returns to column 1.
	w.FocusID(1)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if at := m.pileAt(w); at != 1 {
		t.Fatalf("entered at %d", at)
	}
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0)
	if m.pileAt(w) >= 0 {
		t.Fatal("still in the pile")
	}
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("back on %d", f)
	}
	// Escape leaves the stash as it was.
	m.OverviewMove(-1, 0)
	m.CancelOverview()
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("cancel on %d", f)
	}
}

// A click on a card shows the stash on it.
func TestOverviewPilePick(t *testing.T) {
	m := pileMonitor()
	m.ToggleOverview()
	r := previewOf(t, m.Layout(), 7).Rect
	id := m.overviewAt(float64(r.X+1), float64(r.Y+r.H-1))
	if id != 7 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewPick(id)
	if f, _ := m.Focused(); f != 7 || m.Current().stashHidden {
		t.Fatalf("pick on %d", f)
	}
}

// A workspace with only stashed windows opens with the pile selected, and
// its neighbor's pile is dimmed and unselected.
func TestOverviewPileOnly(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.AddWindow(1)
	w := m.Current()
	w.ToggleWindowStash()
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 1); !p.Focused || p.Hidden {
		t.Fatalf("%+v", p)
	}
	m.CancelOverview()
	m.Focus(1)
	m.AddWindow(2)
	m.Focus(0)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	if p := previewOf(t, m.Layout(), 1); p.Focused || !p.Peek || p.Hidden {
		t.Fatalf("neighbor pile %+v", p)
	}
}

// Fixed overflow's spiral is laid out as a row: no preview overlaps.
func TestOverviewFixedOverflowRow(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowFixed)
	m.SetMaxColumns(2)
	for id := WindowID(1); id <= 4; id++ {
		m.AddWindow(id)
	}
	m.ToggleOverview()
	ps := m.Layout()
	for a := WindowID(1); a <= 4; a++ {
		for b := a + 1; b <= 4; b++ {
			if ra, rb := previewOf(t, ps, a).Rect, previewOf(t, ps, b).Rect; ra.Overlaps(rb) {
				t.Fatalf("%d %+v overlaps %d %+v", a, ra, b, rb)
			}
		}
	}
}
