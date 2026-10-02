package core

import "testing"

// coverMonitor has, on a 300x200 output, tiles 1 and 2 on one workspace,
// 2 fullscreen. With float, 2 is a floating game over tile 1 instead.
func coverMonitor(o Overflow, float bool) *Monitor {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(o)
	m.SetMaxColumns(2)
	m.AddWindow(1)
	if float {
		m.AddFloating(2, 300, 200)
		m.SetFullscreen(2, true)
	} else {
		m.AddWindow(2)
		m.ToggleFullscreen()
	}
	return m
}

// pinnedCases are the setups where the fullscreen window pins its row.
var pinnedCases = []struct {
	name     string
	overflow Overflow
	float    bool
}{
	{"fixed tile", OverflowFixed, false},
	{"fixed float", OverflowFixed, true},
	{"scroll float", OverflowScroll, true},
}

// uniquePlacements fails when a window is placed twice.
func uniquePlacements(t *testing.T, ps []Placement) {
	t.Helper()
	seen := map[WindowID]bool{}
	for _, p := range ps {
		if seen[p.ID] {
			t.Fatalf("window %d placed twice: %+v", p.ID, ps)
		}
		seen[p.ID] = true
	}
}

// The overview over a pinned fullscreen window shows it in front, still
// fullscreen and selected, and the window it hides behind it.
func TestOverviewCoverShowsWorkspace(t *testing.T) {
	for _, tc := range pinnedCases {
		t.Run(tc.name, func(t *testing.T) {
			m := coverMonitor(tc.overflow, tc.float)
			if !m.Current().pinned() {
				t.Fatal("not pinned")
			}
			m.ToggleOverview()
			ps := m.Layout()
			uniquePlacements(t, ps)
			game, behind := previewOf(t, ps, 2), previewOf(t, ps, 1)
			if game.Hidden || !game.Focused || game.Peek || !game.Fullscreen {
				t.Fatalf("game %+v", game)
			}
			if behind.Hidden || !behind.Peek || behind.Focused {
				t.Fatalf("behind %+v", behind)
			}
		})
	}
}

// Return or a click on the fullscreen window keeps it fullscreen.
func TestOverviewCoverConfirmKeeps(t *testing.T) {
	for _, tc := range pinnedCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, pick := range []bool{false, true} {
				m := coverMonitor(tc.overflow, tc.float)
				m.ToggleOverview()
				if pick {
					m.OverviewPick(2)
				} else {
					m.ToggleOverview()
				}
				if f, _ := m.Focused(); f != 2 || m.ov.open || m.Current().fullscreen != 2 || !previewOf(t, m.Layout(), 2).Fullscreen {
					t.Fatalf("pick %v: focused %d, fullscreen %d", pick, f, m.Current().fullscreen)
				}
			}
		})
	}
}

// Choosing the window behind leaves fullscreen and focuses it; the left
// window shows again, a float behind the tiles.
func TestOverviewCoverPickOther(t *testing.T) {
	for _, tc := range pinnedCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, pick := range []bool{false, true} {
				m := coverMonitor(tc.overflow, tc.float)
				m.ToggleOverview()
				if pick {
					m.OverviewPick(1)
				} else {
					m.OverviewMove(-1, 0)
					m.ToggleOverview()
				}
				w := m.Current()
				if f, _ := m.Focused(); f != 1 || m.ov.open || w.fullscreen != 0 {
					t.Fatalf("pick %v: focused %d, fullscreen %d", pick, f, w.fullscreen)
				}
				if p := placement(w, 2); p.Fullscreen || (p.Hidden && !tc.float) {
					t.Fatalf("pick %v: left window %+v", pick, p)
				}
				if i := w.floatIndex(2); tc.float && (i < 0 || !w.Floats[i].below) {
					t.Fatalf("pick %v: float %+v", pick, w.Floats)
				}
			}
		})
	}
}

// Escape returns to the fullscreen window, still fullscreen.
func TestOverviewCoverCancel(t *testing.T) {
	for _, tc := range pinnedCases {
		t.Run(tc.name, func(t *testing.T) {
			m := coverMonitor(tc.overflow, tc.float)
			m.ToggleOverview()
			m.OverviewMove(-1, 0)
			m.CancelOverview()
			if f, _ := m.Focused(); f != 2 || m.Current().fullscreen != 2 {
				t.Fatalf("focused %d, fullscreen %d", f, m.Current().fullscreen)
			}
		})
	}
}

// A stash card is reachable under a fullscreen tile; choosing it leaves
// fullscreen.
func TestOverviewCoverPickStashCard(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowFixed)
	w := m.Current()
	w.AddWindow(1)
	w.AddWindow(5)
	w.FocusID(5)
	w.ToggleWindowStash()
	w.FocusID(1)
	m.ToggleFullscreen()
	if !w.pinned() {
		t.Fatal("not pinned")
	}
	m.ToggleOverview()
	uniquePlacements(t, m.Layout())
	m.OverviewMove(-1, 0)
	if m.card() != 5 {
		t.Fatalf("card %d", m.card())
	}
	m.ToggleOverview()
	if f, _ := m.Focused(); f != 5 || w.fullscreen != 0 {
		t.Fatalf("focused %d, fullscreen %d", f, w.fullscreen)
	}
}

// A fixed fullscreen tile among several columns is the front card, the
// other columns behind it, as for a maximized column.
func TestOverviewCoverTileAmongColumns(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddWindow(2)
	m.AddWindow(3)
	m.Current().FocusID(2)
	m.ToggleFullscreen()
	m.ToggleOverview()
	ps := m.Layout()
	uniquePlacements(t, ps)
	if g := previewOf(t, ps, 2); g.Hidden || g.Peek || !g.Fullscreen {
		t.Fatalf("game %+v", g)
	}
	for _, id := range []WindowID{1, 3} {
		if p := previewOf(t, ps, id); p.Hidden || !p.Peek {
			t.Fatalf("%d: %+v", id, p)
		}
	}
	m.OverviewPick(3)
	if f, _ := m.Focused(); f != 3 || m.Current().fullscreen != 0 {
		t.Fatalf("focused %d, fullscreen %d", f, m.Current().fullscreen)
	}
}
