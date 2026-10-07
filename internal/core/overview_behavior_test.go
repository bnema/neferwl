package core

import (
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOverviewVerticalBoundariesAndInputs(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	m.ToggleOverview()
	if m.stackFront(w) != (stackItem{stackFloat, 9}) {
		t.Fatal("screen front")
	}
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.stackFront(w).kind != stackColumns || m.Active != 0 {
		t.Fatal("swipe did not visit card")
	}
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.Active != 0 {
		t.Fatal("up at top crossed workspace boundary")
	}
	m.overviewFocus(ActionFocusWorkspaceNext)
	if m.stackFront(w).kind != stackFloat || m.Active != 0 {
		t.Fatal("down did not return to front")
	}
	m.OverviewMove(0, 1)
	if m.Active != 1 {
		t.Fatal("down did not reach workspace below")
	}
	m.OverviewMove(0, -1)
	if m.Active != 0 || m.stackFront(w).kind != stackFloat {
		t.Fatal("row change kept stack cursor")
	}
	if !m.overviewScroll(ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, V120: -120}}) || m.stackFront(w).kind != stackColumns {
		t.Fatal("wheel did not visit card")
	}
	m.CancelOverview()
}

func TestOverviewPeekEnvelopeAndSingleCardHorizontal(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.AddFloating(8, 300, 200)
	w.AddFloating(7, 300, 200)
	m.ToggleOverview()
	front := previewOf(t, m.Layout(), 7)
	back := previewOf(t, m.Layout(), 8)
	step := float64(front.Rect.Y - back.Rect.Y)
	if step < float64(w.Usable.H)*w.overviewZoom()*0.06 {
		t.Fatalf("peek step %.1f", step)
	}
	y := w.Usable.Y + (w.Usable.H-w.rowHeight())/2
	if back.Rect.Y < y || front.Rect.Y+front.Rect.H > y+w.rowHeight() {
		t.Fatalf("outside row front %+v back %+v row %d..%d", front, back, y, y+w.rowHeight())
	}
	m.OverviewMove(1, 0)
	if m.stackFront(w) != (stackItem{stackFloat, 7}) {
		t.Fatal("right moved single card")
	}
	m.OverviewMove(-1, 0)
	if m.stackFront(w) != (stackItem{stackFloat, 8}) {
		t.Fatal("left did not send single card behind")
	}
	m.CancelOverview()
}

// The stash front is centred on the same row band as the stack's front,
// whether that row has peeking cards or just one column-group card.
func TestOverviewPileAlignedWithRowFront(t *testing.T) {
	for _, stacked := range []bool{false, true} {
		m := pileMonitor()
		w := m.Current()
		if stacked {
			m.SetBorder(2)
			w.AddFloating(9, 300, 200)
		}
		m.ToggleOverview()
		ps := m.Layout()
		pile := previewOf(t, ps, 7)
		frontID := WindowID(1)
		if stacked {
			frontID = 9
		}
		front := previewOf(t, ps, frontID)
		pileCentre := float64(pile.Rect.Y) + float64(pile.Rect.H)/2
		frontBandCentre := float64(front.Rect.Y) + float64(w.Usable.H)*front.Preview/2
		if math.Abs(pileCentre-frontBandCentre) > 1.5 {
			t.Fatalf("stacked %v: pile centre %.1f, front band centre %.1f: pile %+v front %+v", stacked, pileCentre, frontBandCentre, pile, front)
		}
	}
}

func TestOverviewSingleCardLeftFallsBackToStash(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.FocusID(1)
	w.ToggleWindowStash()
	w.RemoveWindow(2)
	w.RemoveWindow(3)
	// A covering float is the only stack item; stash stays independently reachable.
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.cardAt(w) < 0 {
		t.Fatal("left with no card behind did not enter stash")
	}
	m.CancelOverview()
}

func TestOverviewStackMRUAndSize(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	m.ToggleOverview()
	m.ToggleOverview()
	if got := w.stack(); !slices.Equal(got, []stackItem{{stackColumn, 1}, {kind: stackColumns}}) {
		t.Fatalf("stack %v", got)
	}
	if m.hiddenColumn(w) != 1 {
		t.Fatalf("MRU preselection %d, want Firefox column", m.hiddenColumn(w))
	}
	if p := previewOf(t, m.Layout(), 2); math.Abs(float64(p.Rect.W)-float64(w.columnRectsFor(true)[1].W)*p.Preview) > 1 {
		t.Fatalf("prior maximized buffer not fitted to cell: %+v", p)
	}
	m.CancelOverview()
}

// browsedMonitor has window 1 on workspace 1 and columns 2..5 on workspace
// 2, focused on 5 with one column per screen, and shows workspace 1.
func browsedMonitor(ov Overflow) (*Monitor, *Workspace) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(1)
	m.AddWindow(1)
	m.Focus(1)
	for id := WindowID(2); id <= 5; id++ {
		m.AddWindow(id)
	}
	ws2 := m.Workspaces[1]
	ws2.Overflow = ov
	ws2.FocusID(5)
	ws2.scroll()
	m.Focus(0)
	return m, ws2
}

// Escape gives every row the overview showed its focus and scroll back,
// as first shown, even after leaving and revisiting it.
func TestOverviewBrowsedRowsRestored(t *testing.T) {
	paths := map[string][][2]int{
		"browse":  {{0, 1}, {-1, 0}, {-1, 0}},
		"revisit": {{0, 1}, {-1, 0}, {0, -1}, {0, 1}, {-1, 0}},
	}
	for _, ov := range []Overflow{OverflowScroll, OverflowFixed} {
		for name, path := range paths {
			m, ws2 := browsedMonitor(ov)
			view := ws2.View
			m.ToggleOverview()
			for _, d := range path {
				m.OverviewMove(d[0], d[1])
			}
			m.CancelOverview()
			if id, _ := ws2.Focused(); id != 5 || ws2.View != view || m.Current() != m.Workspaces[0] {
				t.Fatalf("%v %s: focus %d view %v (want %v) current %v", ov, name, id, ws2.View, view, m.Active)
			}
		}
	}
}

func TestOverviewReturnRestoresBrowsedRows(t *testing.T) {
	m, ws2 := browsedMonitor(OverflowScroll)
	m.Focus(2)
	m.AddWindow(6)
	m.Focus(0)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0)
	m.OverviewMove(0, 1)
	if m.Current() != m.Workspaces[2] {
		t.Fatal("not on workspace 3")
	}
	m.ToggleOverview()
	if id, _ := ws2.Focused(); id != 5 || m.Current() != m.Workspaces[2] {
		t.Fatalf("focus %d current %v", id, m.Active)
	}
}
