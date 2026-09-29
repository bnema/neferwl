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
	for _, start := range []struct {
		name   string
		column bool
	}{{"float front", false}, {"columns front", true}} {
		t.Run(start.name, func(t *testing.T) {
			m := stackMonitor()
			w := m.Current()
			if start.column {
				w.FocusID(3)
			}
			before := slices.Clone(order(w))
			m.ToggleOverview()
			ps := m.Layout()
			float, col := previewOf(t, ps, 9), previewOf(t, ps, 3)
			if float.Hidden || col.Hidden || float.Fullscreen || !float.Floating || float.Peek != start.column || col.Peek == start.column {
				t.Fatalf("front/peek: float %+v, column %+v", float, col)
			}
			if !slices.Equal(order(w), before) {
				t.Fatalf("opening changed order: %v", order(w))
			}
			m.OverviewMove(1, 0)
			if start.column {
				if !previewOf(t, m.Layout(), 9).Focused || previewOf(t, m.Layout(), 9).Peek {
					t.Fatal("float not selected")
				}
			} else if !previewOf(t, m.Layout(), 3).Focused || previewOf(t, m.Layout(), 3).Peek {
				t.Fatal("column not selected")
			}
			if !slices.Equal(order(w), before) {
				t.Fatal("rotation committed before Return")
			}
			m.ToggleOverview()
			if start.column {
				wantOrder(t, w, 1, 2, 3, 9)
			} else {
				wantOrder(t, w, 9, 1, 2, 3)
			}
			want := WindowID(3)
			if start.column {
				want = 9
			}
			if id, _ := w.Focused(); id != want {
				t.Fatalf("focus %d, want %d", id, want)
			}
		})
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
		if got := m.overviewAt(float64(x), float64(y)); got != front.ID {
			t.Fatalf("overlap hit %d want %d (front %+v back %+v)", got, front.ID, front.Rect, back.Rect)
		}
		// The up/right exposed corner belongs to the peeking card.
		x, y = back.Rect.X+back.Rect.W-1, back.Rect.Y+1
		if got := m.overviewAt(float64(x), float64(y)); got != back.ID {
			t.Fatalf("peek hit %d want %d", got, back.ID)
		}
		m.OverviewPick(back.ID)
		if id, _ := w.Focused(); id != back.ID || m.overview {
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
		t.Fatalf("dialog shown %+v", p)
	}
	m.OverviewMove(-1, 0)
	if m.cardAt(w) < 0 {
		t.Fatal("left of float did not enter stash")
	}
	m.OverviewMove(1, 0)
	if m.cardAt(w) >= 0 {
		t.Fatal("stash right did not return to columns")
	}
	// First column in front: the next two columns, then the right edge
	// rotates the float to the front.
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0)
	if p := previewOf(t, m.Layout(), 9); p.Peek {
		t.Fatal("right edge did not rotate")
	}
	m.ToggleOverview()
	if p := placement(w, 10); p.Hidden || !slices.Contains(order(w), 10) || order(w)[len(order(w))-1] != 10 {
		t.Fatalf("dialog not above after confirm: %+v %v", p, order(w))
	}

	m = stackMonitor()
	m.Focus(1)
	m.AddFloating(12, 300, 200)
	m.Focus(0)
	m.ToggleOverview()
	p, q := previewOf(t, m.Layout(), 12), previewOf(t, m.Layout(), 4)
	if p.Hidden || q.Hidden || !p.Peek || !q.Peek || p.Focused || q.Focused {
		t.Fatalf("neighbor float %+v column %+v", p, q)
	}
}

func TestOverviewMultipleCoveringCards(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.AddFloating(8, 300, 200)
	w.AddFloating(7, 300, 200)
	w.AddFloating(6, 300, 200)
	m.ToggleOverview()
	ps := m.Layout()
	if previewOf(t, ps, 6).Peek || !previewOf(t, ps, 7).Peek || !previewOf(t, ps, 8).Peek || !previewOf(t, ps, 9).Hidden || !previewOf(t, ps, 3).Hidden {
		t.Fatalf("front and two peeks: %+v", ps)
	}
	m.OverviewMove(1, 0)
	if previewOf(t, m.Layout(), 7).Peek {
		t.Fatal("next float did not rotate forward")
	}
	m.OverviewMove(-1, 0)
	if previewOf(t, m.Layout(), 6).Peek {
		t.Fatal("previous float did not rotate back")
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
	m.OverviewMove(1, 0)
	if m.stackFront(m.Current()) != 0 {
		t.Fatal("columns did not rotate to front")
	}
	m.OverviewMove(0, 1)
	m.OverviewMove(0, -1)
	if m.stackFront(m.Current()) != 9 {
		t.Fatal("row change kept provisional rotation")
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

// Right past the last stash card brings the columns to the front of the
// stack, even when a covering float was in front; Return focuses the first
// column and puts the float below.
func TestOverviewStashRightBringsColumnsFront(t *testing.T) {
	m := stackMonitor()
	w := m.Current()
	w.FocusID(1)
	w.ToggleWindowStash()
	w.FocusID(9)
	m.ToggleOverview()
	m.OverviewMove(-1, 0) // float front, left: the stash
	if m.cardAt(w) < 0 {
		t.Fatal("not in the stash")
	}
	m.OverviewMove(1, 0)
	ps := m.Layout()
	if f, c := previewOf(t, ps, 9), previewOf(t, ps, 2); !f.Peek || c.Peek || !c.Focused {
		t.Fatalf("float %+v, first column %+v", f, c)
	}
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 2 || order(w)[0] != 9 {
		t.Fatalf("focus %d, order %v", id, order(w))
	}
}

// Stack previews are not floats: the overview draws no float veil.
func TestOverviewStackNoFloatVeil(t *testing.T) {
	m := stackMonitor()
	m.ToggleOverview()
	if d := floatDim(m.Layout(), Rect{W: 300, H: 200}, 0.3); d != 0 {
		t.Fatalf("veil %v in the overview", d)
	}
}
