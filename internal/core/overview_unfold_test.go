package core

import "testing"

// cards are the shown stash placements of w in ps, in stash order.
func shownCards(t *testing.T, w *Workspace, ps []Placement) []Placement {
	t.Helper()
	var out []Placement
	for _, e := range w.Stash {
		if p := previewOf(t, ps, e.ID); !p.Hidden {
			out = append(out, p)
		}
	}
	return out
}

// Selected, the cascade pile unfolds into a row under the lane: every card
// side by side, in stash order, none over another, the selection the only
// framed one. Left and right walk it.
func TestOverviewCascadeStashUnfolds(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			withStash(m, 3)
			w := m.Current()
			w.FocusID(10)
			m.ToggleOverview()
			d.move(m, 0, 1)
			at := m.cardAt(w)
			if at < 0 {
				t.Fatal("setup: no card selected")
			}
			ps := m.Layout()
			cards := shownCards(t, w, ps)
			if len(cards) != 3 {
				t.Fatalf("%d cards shown, want 3", len(cards))
			}
			lane := previewOf(t, ps, 10).Rect
			for i, c := range cards {
				if c.Rect.Y <= lane.Y+lane.H {
					t.Fatalf("card %d %v not under the lane %v", c.ID, c.Rect, lane)
				}
				if i > 0 && (c.Rect.Y != cards[0].Rect.Y || c.Rect.X < cards[i-1].Rect.X+cards[i-1].Rect.W) {
					t.Fatalf("cards not side by side: %v then %v", cards[i-1].Rect, c.Rect)
				}
				if c.Focused != (i == at) {
					t.Fatalf("card %d focused %v, selection %d", i, c.Focused, at)
				}
			}
			for i := range 2 {
				d.move(m, -1, 0)
				if want := max(at-i-1, 0); m.Current() != w || m.cardAt(w) != want {
					t.Fatalf("left %d: card %d, want %d", i, m.cardAt(w), want)
				}
				if at-i-1 < 0 {
					break
				}
			}
			for m.cardAt(w) < len(w.Stash)-1 {
				before := m.cardAt(w)
				d.move(m, 1, 0)
				if m.cardAt(w) != before+1 {
					t.Fatalf("right: card %d, was %d", m.cardAt(w), before)
				}
			}
			// Right past the last card goes to the next workspace.
			d.move(m, 1, 0)
			if m.Current() != m.Workspaces[2] {
				t.Fatal("right past the last card did not change workspace")
			}
		})
	}
}

// Down past the last lane enters the stash at its own selection; up leaves
// it, and the pile folds again.
func TestOverviewCascadeStashFolds(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 3)
	w := m.Current()
	w.FocusID(10)
	m.ToggleOverview()
	folded := shownCards(t, w, m.Layout())
	m.OverviewMove(0, 1)
	m.OverviewMove(0, -1)
	if m.cardAt(w) >= 0 {
		t.Fatal("up did not leave the stash")
	}
	again := shownCards(t, w, m.Layout())
	if len(again) != len(folded) {
		t.Fatalf("folded again: %d cards, was %d", len(again), len(folded))
	}
	for i := range again {
		if again[i].Rect != folded[i].Rect || again[i].Focused {
			t.Fatalf("card %d folded at %v (focused %v), was %v", again[i].ID, again[i].Rect, again[i].Focused, folded[i].Rect)
		}
	}
}

// A stash longer than the area scrolls like a wide row, on either axis:
// every card is reached in order, the selection stays inside the area and
// is centred unless the strip stops at an edge of it, and cards past the
// area are hidden.
func TestOverviewStashUnfoldScrolls(t *testing.T) {
	for _, axis := range []layoutAxis{horizontalAxis, verticalAxis} {
		t.Run(map[layoutAxis]string{horizontalAxis: "cascade", verticalAxis: "scroll"}[axis], func(t *testing.T) {
			m := cascadeOverview()
			w := m.Current()
			if axis == verticalAxis {
				w.Overflow = OverflowScroll
			}
			withStash(m, 12)
			m.ToggleOverview()
			if m.overviewAxis() != axis {
				t.Fatal("setup: wrong overview axis")
			}
			m.selectCard(w, 0)
			u := w.overviewArea()
			pos := func(r Rect) (int, int, int, int) {
				if axis == verticalAxis {
					return r.Y, r.H, u.Y, u.Y + u.H
				}
				return r.X, r.W, u.X, u.X + u.W
			}
			centred, hidden := false, false
			for i := range len(w.Stash) {
				if m.cardAt(w) != i {
					t.Fatalf("step %d: card %d", i, m.cardAt(w))
				}
				ps := m.Layout()
				sel := previewOf(t, ps, w.Stash[i].ID)
				at, size, lo, hi := pos(sel.Rect)
				if sel.Hidden || !sel.Focused || at < lo || at+size > hi {
					t.Fatalf("card %d off the area: %+v in %v", i, sel, u)
				}
				first, _, _, _ := pos(previewOf(t, ps, w.Stash[0].ID).Rect)
				last, _, _, _ := pos(previewOf(t, ps, w.Stash[len(w.Stash)-1].ID).Rect)
				if first > lo || last+size < hi {
					t.Fatalf("card %d: strip %d..%d does not fill the area %d..%d", i, first, last+size, lo, hi)
				}
				if mid := at + size/2; abs(mid-(lo+hi)/2) <= 1 {
					centred = true
				} else if first != lo && last+size != hi {
					t.Fatalf("card %d at %d, neither centred on %d nor clamped", i, mid, (lo+hi)/2)
				}
				for _, e := range w.Stash {
					hidden = hidden || previewOf(t, ps, e.ID).Hidden
				}
				if axis == verticalAxis {
					m.OverviewMove(0, 1)
				} else if i < len(w.Stash)-1 {
					m.OverviewMove(1, 0)
				}
			}
			if !hidden || !centred {
				t.Fatalf("12 cards: hidden %v, centred %v; the test does not scroll", hidden, centred)
			}
		})
	}
}

// The stash pile, folded or unfolded, is laid out straight into the
// overview layout every frame: it adds no allocation to the same layout
// without a stash.
func TestOverviewStashLayoutAllocations(t *testing.T) {
	allocs := func(m *Monitor) float64 {
		buf := m.layoutInto(nil)
		return testing.AllocsPerRun(50, func() { buf = m.layoutInto(buf) })
	}
	bare := cascadeOverview()
	bare.ToggleOverview()
	want := allocs(bare)
	for _, unfolded := range []bool{false, true} {
		m := cascadeOverview()
		withStash(m, 6)
		m.ToggleOverview()
		if unfolded {
			m.selectCard(m.Current(), 0)
		}
		if got := allocs(m); got > want {
			t.Errorf("unfolded %v: %v allocs per layout, %v without a stash", unfolded, got, want)
		}
	}
}

// A scroll overview unfolds its pile into a column on the left of the row;
// up and down walk it, right goes back to the columns.
func TestOverviewScrollStashUnfolds(t *testing.T) {
	m := overviewMonitor()
	w := m.Current()
	for id := WindowID(40); id < 43; id++ {
		w.AddWindow(id)
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	w.FocusID(1)
	m.ToggleOverview()
	if m.overviewAxis() != verticalAxis {
		t.Fatal("setup: not a vertical overview")
	}
	m.OverviewMove(-1, 0)
	if m.cardAt(w) < 0 {
		t.Fatal("left from the first column did not select the stash")
	}
	cards := shownCards(t, w, m.Layout())
	if len(cards) != 3 {
		t.Fatalf("%d cards shown, want 3", len(cards))
	}
	for i := 1; i < len(cards); i++ {
		if cards[i].Rect.X != cards[0].Rect.X || cards[i].Rect.Y < cards[i-1].Rect.Y+cards[i-1].Rect.H {
			t.Fatalf("cards not stacked down: %v then %v", cards[i-1].Rect, cards[i].Rect)
		}
	}
	m.OverviewMove(0, -1)
	m.OverviewMove(0, -1)
	if m.cardAt(w) != 0 {
		t.Fatalf("up: card %d, want 0", m.cardAt(w))
	}
	m.OverviewMove(0, 1)
	if m.cardAt(w) != 1 {
		t.Fatalf("down: card %d, want 1", m.cardAt(w))
	}
	m.OverviewMove(1, 0)
	if m.cardAt(w) >= 0 || m.overviewTarget() == 0 {
		t.Fatalf("right: card %d, target %d", m.cardAt(w), m.overviewTarget())
	}
}
