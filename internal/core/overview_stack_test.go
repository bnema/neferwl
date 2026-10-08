package core

import (
	"slices"
	"testing"
)

func stackMonitor() *Monitor {
	m := overviewMonitor()
	m.SetBorder(2)
	m.AddFloating(9, 300, 200)
	return m
}

func TestOverviewCoveringStackNavigation(t *testing.T) {
	for _, columns := range []bool{false, true} {
		m := stackMonitor()
		w := m.Current()
		if columns {
			w.FocusID(3)
		}
		before := slices.Clone(order(w))
		m.ToggleOverview()
		ps := m.Layout()
		f, c := previewOf(t, ps, 9), previewOf(t, ps, 3)
		if f.Peek != columns || c.Peek == columns {
			t.Fatalf("front float %+v columns %+v", f, c)
		}
		if !slices.Equal(order(w), before) {
			t.Fatal("opening changed float order")
		}
		if columns {
			m.OverviewMove(0, -1)
		} else {
			m.OverviewMove(-1, 0)
		}
		if columns {
			if p := previewOf(t, m.Layout(), 9); p.Peek || !p.Focused {
				t.Fatalf("float %+v", p)
			}
		} else {
			if p := previewOf(t, m.Layout(), 3); p.Peek || !p.Focused {
				t.Fatalf("column %+v", p)
			}
		}
		m.ToggleOverview()
		if columns {
			wantOrder(t, w, 1, 2, 3, 9)
		} else {
			wantOrder(t, w, 9, 1, 2, 3)
		}
	}
}

func TestOverviewStackLeftWithoutStashRotatesBackward(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if p := previewOf(t, m.Layout(), 3); p.Peek || !p.Focused {
		t.Fatalf("left did not rotate to columns: %+v", p)
	}
	m.CancelOverview()
	if id, _ := w.Focused(); id != 9 {
		t.Fatalf("cancel focus %d", id)
	}
}

func TestOverviewStackEscapePreservesFocusAndOrder(t *testing.T) {
	for _, columns := range []bool{false, true} {
		m := stackMonitor()
		w := m.Current()
		if columns {
			w.FocusID(3)
		}
		before := slices.Clone(order(w))
		focused, _ := w.Focused()
		m.ToggleOverview()
		m.OverviewMove(1, 0)
		m.CancelOverview()
		if id, _ := w.Focused(); id != focused || !slices.Equal(order(w), before) {
			t.Fatalf("columns %v: focus %d want %d, order %v want %v", columns, id, focused, order(w), before)
		}
	}
	// When the focused float is not last (a dialog sits over it), Escape
	// must restore its position too; FocusID alone would raise it.
	m := stackMonitor()
	w := m.Current()
	w.AddFloating(10, 20, 20)
	w.FocusID(9)
	before := slices.Clone(order(w))
	m.ToggleOverview()
	m.OverviewMove(1, 0)
	m.CancelOverview()
	if id, _ := w.Focused(); id != 9 || !slices.Equal(order(w), before) {
		t.Fatalf("float focus %d order %v want %v", id, order(w), before)
	}
}

func TestOverviewStackClickAndHitPriority(t *testing.T) {
	for _, frontColumn := range []bool{false, true} {
		m := stackMonitor()
		w := m.Current()
		if frontColumn {
			w.FocusID(3)
		}
		m.ToggleOverview()
		ps := m.Layout()
		front, back := previewOf(t, ps, 9), previewOf(t, ps, 3)
		if frontColumn {
			front, back = back, front
		}
		// The overlap with the peeking column tile must hit the front.
		x, y := back.Rect.X+back.Rect.W/2, max(front.Rect.Y, back.Rect.Y)+back.Rect.H/2
		if frontColumn {
			x = front.Rect.X + front.Rect.W/2
		}
		if got := overviewIn(m.Layout(), float64(x), float64(y)); got != front.ID {
			t.Fatalf("overlap hit %d want %d (front %+v back %+v)", got, front.ID, front.Rect, back.Rect)
		}
		// The up/right exposed corner belongs to the peeking card.
		x, y = back.Rect.X+back.Rect.W-1, back.Rect.Y+1
		if got := overviewIn(m.Layout(), float64(x), float64(y)); got != back.ID {
			t.Fatalf("peek hit %d want %d", got, back.ID)
		}
		m.OverviewPick(back.ID)
		if id, _ := w.Focused(); id != back.ID || m.ov.open {
			t.Fatalf("picked %d, focus %d", back.ID, id)
		}
	}
}

func TestOverviewStackStashDialogNeighbor(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.AddFloating(10, 20, 20)
	w.FocusID(9)
	w.AddWindow(11)
	w.FocusID(11)
	w.ToggleWindowStash()
	w.FocusID(9)
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 10); !p.Hidden {
		t.Fatalf("dialog %+v", p)
	}
	m.OverviewMove(-1, 0) // single-window float sends behind before stash
	if m.stackFront(w).kind != stackColumns || m.cardAt(w) >= 0 {
		t.Fatal("float did not send behind")
	}
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0)
	if m.cardAt(w) < 0 {
		t.Fatal("left from first column did not enter stash")
	}
	m.OverviewMove(1, 0)
	if m.cardAt(w) >= 0 {
		t.Fatal("stash exit did not return to card")
	}
	m.CancelOverview()
	m = stackMonitor()
	m.Focus(1)
	m.AddFloating(12, 300, 200)
	m.Focus(0)
	m.ToggleOverview()
	if p, q := previewOf(t, m.Layout(), 12), previewOf(t, m.Layout(), 4); p.Hidden || q.Hidden || !p.Peek || !q.Peek || p.Focused || q.Focused {
		t.Fatalf("neighbor %+v %+v", p, q)
	}
}

func TestOverviewMultipleCoveringCards(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.AddFloating(8, 300, 200)
	w.AddFloating(7, 300, 200)
	w.AddFloating(6, 300, 200)
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 6); p.Peek {
		t.Fatalf("front %+v", p)
	}
	if p := previewOf(t, m.Layout(), 7); !p.Peek {
		t.Fatalf("peek %+v", p)
	}
	if p := previewOf(t, m.Layout(), 9); !p.Hidden {
		t.Fatalf("far card %+v", p)
	}
	m.OverviewMove(0, -1)
	if p := previewOf(t, m.Layout(), 7); p.Peek || !p.Focused {
		t.Fatalf("up %+v", p)
	}
	if p := previewOf(t, m.Layout(), 6); !p.Peek || p.Rect.Y <= previewOf(t, m.Layout(), 7).Rect.Y {
		t.Fatalf("passed card %+v", p)
	}
	m.OverviewMove(0, 1)
	if p := previewOf(t, m.Layout(), 6); p.Peek {
		t.Fatal("down did not return to screen front")
	}
	m.CancelOverview()
}

func TestOverviewFloatWithoutColumns(t *testing.T) {
	m := monitor()
	m.SetOutput(300, 200)
	m.SetBorder(2)
	w := m.Current()
	w.AddFloating(9, 300, 200)
	w.AddFloating(8, 300, 200)
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 8); p.Peek || p.Hidden {
		t.Fatalf("front float %+v", p)
	}
	m.OverviewMove(-1, 0)
	if p := previewOf(t, m.Layout(), 9); p.Peek || p.Hidden {
		t.Fatalf("back float %+v", p)
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 9 {
		t.Fatalf("focus %d", id)
	}
}

func TestOverviewStackRowChangeResetsRotation(t *testing.T) {
	m := stackMonitor()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	if m.stackFront(m.Current()).kind != stackColumns {
		t.Fatal("stack did not move to columns")
	}
	m.OverviewMove(0, -1) // at stack end: workspace above, if present
	m.OverviewMove(0, 1)
	if m.stackFront(m.Current()) != (stackItem{stackFloat, 9}) {
		t.Fatal("row change retained cursor")
	}
	m.CancelOverview()
}

func TestOverviewStackInvalidatedOnRemoval(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(1, 0)
	w.RemoveWindow(9)
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 3 {
		t.Fatalf("removed selection focused %d", id)
	}
}

// Stash navigation returns to the current front card without changing its cursor.
func TestOverviewStashRightBringsColumnsFront(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.FocusID(1)
	w.ToggleWindowStash()
	w.FocusID(9)
	m.ToggleOverview()
	m.OverviewMove(-1, 0) // float to columns
	m.OverviewMove(-1, 0) // first column to stash
	if m.cardAt(w) < 0 {
		t.Fatal("not in stash")
	}
	m.OverviewMove(1, 0)
	if m.cardAt(w) >= 0 || m.stackFront(w).kind != stackColumns {
		t.Fatal("not returned to front column card")
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("focus %d", id)
	}
}

// Stack previews are not floats: the overview draws no float veil.
func TestOverviewStackNoFloatVeil(t *testing.T) {
	m := stackMonitor()
	m.ToggleOverview()
	if d := floatDim(m.Layout(), whole(Rect{W: 300, H: 200}), 0.3); d != 0 {
		t.Fatalf("veil %v in the overview", d)
	}
}

// l leaves the pile for the front card, even with no columns.
func TestOverviewPileRightToFloatCard(t *testing.T) {
	m := monitor()
	m.SetOutput(300, 200)
	m.SetBorder(2)
	w := m.Current()
	m.AddWindow(1)
	w.ToggleWindowStash()
	w.ToggleStashVisible()
	w.AddFloating(9, 300, 200)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.card() != 1 {
		t.Fatalf("h did not enter the pile: card %d", m.card())
	}
	m.OverviewMove(1, 0)
	if m.card() != 0 || m.overviewTarget() != 9 {
		t.Fatalf("l stayed in the pile: card %d target %d", m.card(), m.overviewTarget())
	}
}
