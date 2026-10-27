package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// stashMonitor has tiles 1, 2, 3 with 2 and 3 stashed, 3 selected.
func stashMonitor(overflow Overflow) *Monitor {
	m := newMonitor("", "")
	m.SetOutput(100, 80)
	m.SetOverflow(overflow)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	for _, id := range []WindowID{2, 3} {
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	return m
}

// A stashed window moved to another workspace joins the end of its stash,
// selected, and returns there as a last column.
func TestStashMoveToWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
			m := stashMonitor(overflow)
			w := m.Current()
			m.MoveToWorkspace(1, false)
			to := m.Workspaces[1]
			if !slices.Equal(stashIDs(w), []WindowID{2}) || !slices.Equal(stashIDs(to), []WindowID{3}) || to.StashAt() != 0 || to.Inspect().StashHidden {
				t.Fatalf("%v: from %v to %v", overflow, stashIDs(w), stashIDs(to))
			}
			if id, _ := w.Focused(); id != 2 {
				t.Fatalf("%v: source focus %d", overflow, id)
			}
			to.AddWindow(9)
			to.FocusID(3)
			to.ToggleWindowStash()
			if len(to.Columns) != 2 || to.Columns[1].Windows[0] != 3 {
				t.Fatalf("%v: unstash elsewhere %+v", overflow, to.Columns)
			}
		}

	})
}

// A workspace moved to another monitor keeps its stash, selection and
// hidden state.
func TestStashFollowsWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := stashMonitor(OverflowScroll)
		w := m.Current()
		w.FocusID(2)
		w.ToggleStashVisible()
		m.take(w)
		dst := newMonitor("", "")
		dst.SetOutput(200, 100)
		dst.adopt(w, false, 0)
		if !slices.Equal(stashIDs(w), []WindowID{2, 3}) || w.StashAt() != 0 || !w.Inspect().StashHidden {
			t.Fatalf("stash %v at %d hidden %v", stashIDs(w), w.StashAt(), w.Inspect().StashHidden)
		}
		w.ToggleStashVisible()
		if p := placement(w, 2); p.Rect != (Rect{X: 20, Y: 10, W: 160, H: 80}) {
			t.Fatalf("placement on the new output %+v", p)
		}

	})
}

// A stashed window goes fullscreen in place and stays in its stash slot,
// selected when the user was on it; a client exit while the user is on
// another workspace leaves the view there.
func TestStashFullscreenRoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := stashMonitor(OverflowFixed)
		w := m.Current()
		w.FocusID(2)
		m.ToggleFullscreen()
		if m.Current() != w || w.Cover() != 2 || !slices.Equal(stashIDs(w), []WindowID{2, 3}) {
			t.Fatalf("fullscreen: cover %d stash %v", w.Cover(), stashIDs(w))
		}
		m.ToggleFullscreen()
		if id, _ := w.Focused(); id != 2 || w.StashAt() != 0 || w.Fullscreen != 0 {
			t.Fatalf("focused %d at %d", id, w.StashAt())
		}
		m.ToggleFullscreen()
		m.Focus(1)
		m.SetFullscreen(2, false)
		if !slices.Equal(stashIDs(w), []WindowID{2, 3}) || w.Fullscreen != 0 || m.Active != 1 {
			t.Fatalf("request: %v on %d", stashIDs(w), m.Active)
		}

	})
}

// A shown stash without tiles has the focus, however it got there: a
// named workspace handing its windows over, or a fullscreen window
// returning while the user is elsewhere.
func TestStashAloneHasFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := stashMonitor(OverflowFixed)
		w := m.Current()
		w.FocusID(1)
		w.RemoveWindow(1) // only stashed windows left, the tile had the focus
		if id, _ := w.Focused(); id != 3 {
			t.Fatalf("focused %d, want the selected stashed window", id)
		}
		// Fixed overflow: the stashed window goes fullscreen, its only tile
		// closes, then the client leaves fullscreen.
		m = newMonitor("", "")
		m.SetOutput(100, 80)
		m.SetOverflow(OverflowFixed)
		m.AddWindow(1)
		m.AddWindow(2)
		w = m.Current()
		w.ToggleWindowStash()
		m.ToggleFullscreen()
		if w.Cover() != 2 {
			t.Fatal("not fullscreen")
		}
		m.RemoveWindow(1)
		m.SetFullscreen(2, false)
		if len(w.Columns) != 0 || !slices.Equal(stashIDs(w), []WindowID{2}) {
			t.Fatalf("workspace %+v", w)
		}
		if id, ok := w.Focused(); !ok || id != 2 {
			t.Fatalf("focused %d %v", id, ok)
		}

	})
}

// A window opening under a fullscreen stashed window, with no tiles left,
// waits as the only column.
func TestStashFullscreenWithoutTilesAddWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitor("", "")
		m.SetOutput(100, 80)
		m.AddWindow(1)
		w := m.Current()
		w.ToggleWindowStash()
		w.ToggleFullscreen()
		w.AddWindow(2)
		if len(w.Columns) != 1 || w.Columns[0].Windows[0] != 2 || w.Cover() != 1 {
			t.Fatalf("columns %+v cover %d", w.Columns, w.Cover())
		}

	})
}

// A removed named workspace hands its stash to the active one; with no
// tiles there, the stash takes the focus.
func TestStashFromRemovedNamedWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitor("", "")
		m.SetOutput(100, 80)
		m.SetNamed([]NamedWorkspace{{Name: "notes"}})
		m.ToggleNamed("notes")
		m.AddWindow(1)
		m.AddWindow(2)
		w := m.Current()
		w.ToggleWindowStash()
		w.FocusID(1)
		w.ToggleWindowStash()
		m.SetNamed(nil)
		to := m.Workspaces[m.Active]
		if !slices.Equal(stashIDs(to), []WindowID{2, 1}) {
			t.Fatalf("stash %v", stashIDs(to))
		}
		if id, ok := to.Focused(); !ok || id != 2 {
			t.Fatalf("focused %d %v", id, ok)
		}

	})
}

// With stash.capture, a window opened while the stash is shown joins its
// end, selected, and takes the focus unless a native dialog has it. A
// hidden stash, a covering fullscreen window or capture off let it tile.
func TestStashCapture(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow Overflow
		setup    func(*Monitor)
		captured bool
		focused  WindowID
	}{
		{"shown", OverflowScroll, func(*Monitor) {}, true, 4},
		{"shown fixed", OverflowFixed, func(*Monitor) {}, true, 4},
		{"dialog keeps focus", OverflowScroll, func(m *Monitor) { m.AddFloating(9, 10, 10) }, true, 9},
		{"hidden", OverflowScroll, func(m *Monitor) { m.Current().ToggleStashVisible() }, false, 4},
		{"under fullscreen", OverflowScroll, func(m *Monitor) { m.Current().SetFullscreen(3, true) }, false, 3},
		{"off", OverflowScroll, func(m *Monitor) { m.SetStashCapture(false) }, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := stashMonitor(tc.overflow)
				m.SetStashCapture(true)
				tc.setup(m)
				w := m.Current()
				m.AddWindow(4)
				if captured := w.StashIndex(4) == 2 && w.StashAt() == 2; captured != tc.captured {
					t.Fatalf("captured %v, stash %+v", captured, w.Stash)
				}
				if id, _ := w.Focused(); id != tc.focused {
					t.Fatalf("focused %d", id)
				}

			})
		})
	}
}

// A captured window has no former column: toggle-window-stash tiles it as
// a new column after the focused one.
func TestStashCaptureUnstash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := stashMonitor(OverflowScroll)
		m.SetStashCapture(true)
		w := m.Current()
		m.AddWindow(4)
		w.ToggleWindowStash()
		if len(w.Columns) != 2 || w.Columns[1].Windows[0] != 4 || w.Focus != 1 || w.StashIndex(4) >= 0 {
			t.Fatalf("columns %+v focus %d stash %+v", w.Columns, w.Focus, w.Stash)
		}

	})
}

// stash.width sets the selected window's width; the neighbors sit
// stash.gap beside it, in what the margins show.
func TestStashWidth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := stashMonitor(OverflowScroll)
		m.SetStash(50, 10)
		w := m.Current()
		if p := placement(w, 3); p.Rect != (Rect{X: 25, Y: 8, W: 50, H: 64}) {
			t.Fatalf("selected %+v", p)
		}
		// 25 margin - 10 gap: 15 of it shows, then the gap, then the window.
		if p := placement(w, 2); !p.Peek || p.Rect != (Rect{X: 15 - 50, Y: 8, W: 50, H: 64}) {
			t.Fatalf("left peek %+v", p)
		}
		// At 90%, a 2% gap leaves 3% of each neighbor.
		m.SetStash(90, 2)
		if p := placement(w, 2); !p.Peek || p.Rect.X+p.Rect.W != 3 {
			t.Fatalf("narrow peek %+v", p)
		}
		// A gap wider than the margin hides the neighbors.
		m.SetStash(90, 10)
		if p := placement(w, 2); !p.Hidden {
			t.Fatalf("peek past the gap %+v", p)
		}
		// 0 is the default, out-of-range values are clamped.
		m.SetStash(0, 3)
		if p := placement(w, 3); p.Rect.W != 80 {
			t.Fatalf("default width %d", p.Rect.W)
		}
		m.SetStash(5, 3)
		if p := placement(w, 3); p.Rect.W != 10 {
			t.Fatalf("clamped low %d", p.Rect.W)
		}
		m.SetStash(100, 3)
		if p := placement(w, 3); p.Rect.W != 90 {
			t.Fatalf("clamped high %d", p.Rect.W)
		}

	})
}

// focus-column from a fullscreen stashed window at the end of the stash
// goes to the neighbor monitor and keeps fullscreen; inside the stash it
// leaves fullscreen for the next stashed window.
func TestFullscreenStashEdgeGoesToMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := ports.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		cfg.Layout.Overflow = string(OverflowFixed)
		c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c.addScreen(ports.OutputInfo{Name: "A", Width: 100, Height: 80})
		c.addScreen(ports.OutputInfo{Name: "B", Width: 100, Height: 80})
		m := c.screens[0].mon
		for id := WindowID(1); id <= 3; id++ {
			m.AddWindow(id)
		}
		w := m.Current()
		w.FocusID(2)
		w.ToggleWindowStash()
		w.FocusID(3)
		w.ToggleWindowStash() // stash [2 3], 3 selected
		m.ToggleFullscreen()
		if w.Cover() != 3 {
			t.Fatal("setup", w.Cover())
		}
		c.applyAction(ActionFocusColumnRight)
		if c.focusScreen != 1 || w.Cover() != 3 {
			t.Fatalf("edge: screen %d cover %d", c.focusScreen, w.Cover())
		}
		c.focusScreen = 0
		c.applyAction(ActionFocusColumnLeft)
		if id, _ := w.Focused(); id != 2 || w.Fullscreen != 0 || c.focusScreen != 0 {
			t.Fatalf("inside: focused %d fullscreen %d", id, w.Fullscreen)
		}

	})
}
