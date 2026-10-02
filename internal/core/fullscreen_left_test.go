package core

import "testing"

// After the user leaves a window's fullscreen for another window, the
// client's next fullscreen request is refused until the window is focused
// again, whichever way the user left: a focus move, an activation or the
// overview.
func TestLeftFullscreenRefusesRequest(t *testing.T) {
	leaves := map[string]func(m *Monitor){
		"focus move": func(m *Monitor) { m.Current().FocusColumn(-1) },
		"activate":   func(m *Monitor) { m.Current().Activate(1) },
		"overview": func(m *Monitor) {
			m.ToggleOverview()
			m.OverviewPick(1)
		},
	}
	// Fixed overflow refuses an unfocused window's request anyway; scroll
	// with a float is where the latch alone refuses it.
	for _, tc := range pinnedCases {
		for name, leave := range leaves {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				m := coverMonitor(tc.overflow, tc.float)
				w := m.Current()
				leave(m)
				if f, _ := m.Focused(); f != 1 || w.fullscreen != 0 || w.left != 2 {
					t.Fatalf("leave: focused %d, fullscreen %d, left %d", f, w.fullscreen, w.left)
				}
				m.SetFullscreen(2, true)
				if f, _ := m.Focused(); f != 1 || w.fullscreen != 0 {
					t.Fatalf("re-request: focused %d, fullscreen %d", f, w.fullscreen)
				}
				w.FocusID(2)
				m.SetFullscreen(2, true)
				if w.fullscreen != 2 || w.left != 0 {
					t.Fatalf("focused again: fullscreen %d, left %d", w.fullscreen, w.left)
				}
			})
		}
	}
}

// The bind leaves a window's own fullscreen without the latch: the app
// may ask again at once.
func TestToggleFullscreenOffNoLatch(t *testing.T) {
	m := coverMonitor(OverflowFixed, false)
	w := m.Current()
	m.ToggleFullscreen()
	if w.fullscreen != 0 || w.left != 0 {
		t.Fatalf("fullscreen %d, left %d", w.fullscreen, w.left)
	}
	m.SetFullscreen(2, true)
	if w.fullscreen != 2 {
		t.Fatalf("request refused: %d", w.fullscreen)
	}
}

// Focusing the window again drops the latch even without a request, so a
// later request while unfocused follows the usual rules again.
func TestLeftFullscreenSettlesOnFocus(t *testing.T) {
	m := coverMonitor(OverflowScroll, true)
	w := m.Current()
	w.Activate(1)
	w.FocusID(2)
	w.settleLeft()
	if w.left != 0 {
		t.Fatalf("left %d", w.left)
	}
	w.FocusID(1)
	m.SetFullscreen(2, true)
	if w.fullscreen != 2 {
		t.Fatalf("request refused: %d", w.fullscreen)
	}
}

// A taskbar fullscreen request activates the window first (core), so it
// passes the latch.
func TestLeftFullscreenTaskbar(t *testing.T) {
	m := coverMonitor(OverflowScroll, true)
	w := m.Current()
	w.Activate(1)
	w.Activate(2)
	m.SetFullscreen(2, true)
	if w.fullscreen != 2 || w.left != 0 {
		t.Fatalf("fullscreen %d, left %d", w.fullscreen, w.left)
	}
}

// A latched window moved away with its column, which another window of
// it has the focus of, drops the latch.
func TestLeftFullscreenColumnMoved(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{2, 3}, []WindowID{1})
	w.FocusID(2)
	w.fullscreen = 2
	w.Activate(3)
	if w.left != 2 {
		t.Fatalf("latch %d", w.left)
	}
	if _, ok := w.takeColumn(); !ok || w.left != 0 {
		t.Fatalf("left %d", w.left)
	}
}

// A latched window that goes away drops the latch.
func TestLeftFullscreenRemoved(t *testing.T) {
	m := coverMonitor(OverflowScroll, true)
	w := m.Current()
	w.Activate(1)
	w.RemoveWindow(2)
	if w.left != 0 {
		t.Fatalf("left %d", w.left)
	}
}

// Activating a hidden stashed window leaves another window's fullscreen
// with the latch too.
func TestActivateHiddenStashLatches(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowFixed)
	w := m.Current()
	w.AddWindow(1)
	w.AddWindow(5)
	w.FocusID(5)
	w.ToggleWindowStash()
	w.ToggleStashVisible()
	w.FocusID(1)
	m.ToggleFullscreen()
	w.Activate(5)
	if f, _ := w.Focused(); f != 5 || w.fullscreen != 0 || w.left != 1 {
		t.Fatalf("focused %d, fullscreen %d, left %d", f, w.fullscreen, w.left)
	}
}
