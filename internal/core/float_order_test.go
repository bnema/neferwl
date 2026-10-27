package core

import (
	"testing"
	"testing/synctest"
)

func TestDemotedFloatFullscreenRoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetBorder(2)
		for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
			m = monitor()
			m.SetBorder(2)
			m.Current().Overflow = overflow
			m.AddWindow(1)
			m.AddFloating(2, 100, 80)
			w := m.Current()
			w.FocusID(1)
			wantOrder(t, w, 2, 1)
			m.SetFullscreen(2, true)
			// Fixed overflow refuses an unfocused window's fullscreen (ADR 011).
			if p := placement(w, 2); p.Fullscreen != (overflow == OverflowScroll) {
				t.Fatalf("%v: fullscreen %+v", overflow, p)
			}
			m.SetFullscreen(2, false)
			wantOrder(t, w, 2, 1)
			if id, _ := w.Focused(); id != 1 {
				t.Fatalf("focus %d", id)
			}
		}

	})
}

func TestDemotedFloatMoveAndAdopt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetBorder(2)
		m.AddWindow(1)
		m.AddFloating(2, 100, 80)
		w := m.Current()
		w.FocusID(1)
		other := monitor()
		other.SetOutput(102, 82)
		other.SetBorder(2)
		m.take(w)
		other.adopt(w, false, 0)
		if !w.Inspect().Floats[0].Below {
			t.Fatal("adopt raised float")
		}
		wantOrder(t, w, 2, 1)
		// A user moving the selected float to a workspace raises it there.
		other.show(w)
		w.FocusID(2)
		other.MoveToWorkspace(1, false)
		if w.Has(2) || !other.Workspaces[1].Has(2) {
			t.Fatal("move lost float")
		}

	})
}

// Up from the top of a column raises the last demoted covering float;
// down leaves it for the columns again without changing workspaces.
func TestFocusWindowReturnsToDemotedCoveringFloat(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := monitor()
				m.SetOverflow(overflow)
				m.SetBorder(2)
				m.AddWindow(1)            // kitty
				m.AddFloating(2, 100, 80) // covering game
				m.AddFloating(3, 100, 80) // last demoted float is on top
				w := m.Current()
				w.FocusID(1)
				wantOrder(t, w, 2, 3, 1)
				m.Apply(ActionFocusWindowUp)
				if id, _ := w.Focused(); id != 3 || m.Active != 0 || w.Inspect().Floats[len(w.Floats)-1].Below {
					t.Fatalf("up focused %d on workspace %d, floats %+v", id, m.Active, w.Floats)
				}
				wantOrder(t, w, 2, 1, 3)
				m.Apply(ActionFocusWindowDown)
				if id, _ := w.Focused(); id != 1 || m.Active != 0 {
					t.Fatalf("down focused %d on workspace %d", id, m.Active)
				}
				wantOrder(t, w, 2, 3, 1)

			})
		})
	}
}

func TestFocusWindowDemotedFloatAfterColumnAndScreenNeighbors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetOverflow(OverflowFixed)
		m.SetBorder(2)
		m.AddWindow(1)
		m.AddWindow(2)
		w := m.Current()
		w.ConsumeOrExpel(-1) // two windows in the same column
		w.AddFloating(9, 100, 80)
		w.FocusID(1) // top of the column
		w.FocusID(2) // bottom of the column
		if !w.FocusWindow(-1) {
			t.Fatal("up inside column failed")
		}
		if id, _ := w.Focused(); id != 1 || !w.Inspect().Floats[0].Below {
			t.Fatalf("up inside column focused %d, float %+v", id, w.Floats[0])
		}
		if !w.FocusWindow(-1) {
			t.Fatal("up to float failed")
		}
		if id, _ := w.Focused(); id != 9 {
			t.Fatalf("up to float focused %d", id)
		}

		m = monitor()
		m.SetOverflow(OverflowFixed)
		m.SetMaxColumns(2)
		for id := WindowID(1); id <= 4; id++ {
			m.AddWindow(id)
		}
		w = m.Current()
		w.AddFloating(9, 100, 80)
		w.FocusID(4)
		if !w.FocusWindow(-1) {
			t.Fatal("up to on-screen column failed")
		}
		if id, _ := w.Focused(); id != 2 || !w.Inspect().Floats[0].Below {
			t.Fatalf("up focused %d, float %+v", id, w.Floats[0])
		}

	})
}

func TestFocusWindowWithoutDemotedFloatChangesWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.AddWindow(1)
		m.Focus(1)
		m.AddWindow(2)
		m.Apply(ActionFocusWindowUp)
		if m.Active != 0 {
			t.Fatalf("up on workspace %d", m.Active)
		}

	})
}

// Up from a shown stash stays in the stash, even with a demoted covering
// float; from a fullscreen float it leaves fullscreen for the tile below.
func TestFocusWindowUpStashAndPinned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetBorder(2)
		m.AddWindow(1)
		m.AddWindow(2)
		w := m.Current()
		w.AddFloating(9, 100, 80)
		w.FocusID(2)
		w.ToggleWindowStash() // 2 to the shown stash, focused
		if !w.Inspect().Floats[0].Below {
			t.Fatalf("float not below: %+v", w.Floats[0])
		}
		m.Apply(ActionFocusWindowUp)
		if id, _ := w.Focused(); id != 2 || !w.Inspect().Floats[0].Below || m.Active != 0 {
			t.Fatalf("stash up focused %d on %d, float %+v", id, m.Active, w.Floats[0])
		}

		m = monitor()
		m.SetOverflow(OverflowFixed)
		m.AddWindow(1)
		m.AddFloating(2, 50, 40)
		m.SetFullscreen(2, true)
		if m.Active != 0 || !m.Current().Pinned() {
			t.Fatalf("setup: active %d", m.Active)
		}
		m.Apply(ActionFocusWindowUp)
		if id, _ := m.Current().Focused(); id != 1 || m.Active != 0 || m.Current().Fullscreen != 0 {
			t.Fatalf("pinned up focused %d on %d", id, m.Active)
		}

	})
}

// A tile leaving fullscreen with the focus comes back in front of a
// covering float mapped meanwhile.
func TestFullscreenReturnRaisesColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, stacked := range []bool{false, true} {
			m := monitor()
			m.SetBorder(2)
			m.SetOverflow(OverflowFixed)
			m.AddWindow(1)
			m.AddWindow(2)
			if stacked {
				m.Current().ConsumeOrExpel(-1)
			}
			w := m.Current()
			w.FocusID(2)
			m.ToggleFullscreen()
			w.AddFloating(3, 100, 80)
			m.ToggleFullscreen() // back, focused
			if id, _ := w.Focused(); id != 2 {
				t.Fatalf("stacked %v: focus %d", stacked, id)
			}
			if got := order(w); got[0] != 3 {
				t.Fatalf("stacked %v: order %v, covering float not below", stacked, got)
			}
		}

	})
}
