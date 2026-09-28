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

// A click picks the preview under it; stashed windows stay hidden.
func TestOverviewPickAndStash(t *testing.T) {
	m := overviewMonitor()
	m.Current().FocusID(1)
	m.Current().ToggleWindowStash()
	m.ToggleOverview()
	ps := m.Layout()
	if !previewOf(t, ps, 1).Hidden {
		t.Fatal("stashed window shown in overview")
	}
	below := previewOf(t, ps, 4).Rect
	id := m.overviewAt(float64(below.X+1), float64(below.Y+1))
	if id != 4 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewPick(id)
	if f, _ := m.Focused(); f != 4 || m.overview {
		t.Fatalf("pick: %d", f)
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
