package core

import (
	"slices"
	"testing"
)

// stashIDs lists the stash of w, left to right.
func stashIDs(w *Workspace) []WindowID {
	var ids []WindowID
	for _, f := range w.Stash {
		ids = append(ids, f.ID)
	}
	return ids
}

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
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		m := stashMonitor(overflow)
		w := m.Current()
		m.MoveToWorkspace(1, false)
		to := m.Workspaces[1]
		if !slices.Equal(stashIDs(w), []WindowID{2}) || !slices.Equal(stashIDs(to), []WindowID{3}) || to.stashAt != 0 || to.stashHidden {
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
}

// A workspace moved to another monitor keeps its stash, selection and
// hidden state.
func TestStashFollowsWorkspace(t *testing.T) {
	m := stashMonitor(OverflowScroll)
	w := m.Current()
	w.FocusID(2)
	w.ToggleStashVisible()
	m.take(w)
	dst := newMonitor("", "")
	dst.SetOutput(200, 100)
	dst.adopt(w, false, 0)
	if !slices.Equal(stashIDs(w), []WindowID{2, 3}) || w.stashAt != 0 || !w.stashHidden {
		t.Fatalf("stash %v at %d hidden %v", stashIDs(w), w.stashAt, w.stashHidden)
	}
	w.ToggleStashVisible()
	if p := placement(w, 2); p.Rect != (Rect{X: 20, Y: 10, W: 160, H: 80}) {
		t.Fatalf("placement on the new output %+v", p)
	}
}

// Fixed overflow gives a fullscreen window its own workspace: a stashed
// one returns to its place in the stash, selected when the user was on it.
func TestStashFullscreenRoundTrip(t *testing.T) {
	m := stashMonitor(OverflowFixed)
	w := m.Current()
	w.FocusID(2)
	m.ToggleFullscreen()
	if m.Current() == w || !slices.Equal(stashIDs(w), []WindowID{3}) {
		t.Fatalf("fullscreen stayed: %v", stashIDs(w))
	}
	m.ToggleFullscreen()
	if m.Current() != w || !slices.Equal(stashIDs(w), []WindowID{2, 3}) {
		t.Fatalf("back %v", stashIDs(w))
	}
	if id, _ := w.Focused(); id != 2 || w.stashAt != 0 {
		t.Fatalf("focused %d at %d", id, w.stashAt)
	}
	// Left by a client request while the user is elsewhere: the selection
	// stays on the same window.
	w.FocusID(2)
	m.ToggleFullscreen()
	fs := m.Current()
	m.Focus(indexOf(m.Workspaces, w))
	w.FocusID(3)
	m.SetFullscreen(2, false)
	if !slices.Equal(stashIDs(w), []WindowID{2, 3}) || w.Stash[w.stashAt].ID != 3 || m.has(fs) {
		t.Fatalf("request: %v at %d", stashIDs(w), w.stashAt)
	}
}

// A shown stash without tiles has the focus, however it got there: a
// named workspace handing its windows over, or a fullscreen window
// returning while the user is elsewhere.
func TestStashAloneHasFocus(t *testing.T) {
	m := stashMonitor(OverflowFixed)
	w := m.Current()
	w.FocusID(1)
	w.RemoveWindow(1) // only stashed windows left, the tile had the focus
	if id, _ := w.Focused(); id != 3 {
		t.Fatalf("focused %d, want the selected stashed window", id)
	}
	// Fixed overflow: the stashed window goes fullscreen on its own
	// workspace, its only tile closes, then the client leaves fullscreen
	// while the user is elsewhere.
	m = newMonitor("", "")
	m.SetOutput(100, 80)
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddWindow(2)
	w = m.Current()
	w.ToggleWindowStash()
	m.ToggleFullscreen()
	fs := m.Current()
	if fs == w || fs.origin != w {
		t.Fatal("no fullscreen workspace")
	}
	m.RemoveWindow(1)
	m.Focus(indexOf(m.Workspaces, fs) + 1)
	m.SetFullscreen(2, false)
	if len(w.Columns) != 0 || !slices.Equal(stashIDs(w), []WindowID{2}) || m.has(fs) {
		t.Fatalf("origin %+v", w)
	}
	if id, ok := w.Focused(); !ok || id != 2 {
		t.Fatalf("focused %d %v", id, ok)
	}
}

// A fullscreen request made while the stash was hidden applies on show,
// even over an off-screen fullscreen tile.
func TestStashPendingFullscreenOverTile(t *testing.T) {
	w := workspace()
	w.SetMaxColumns(1)
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddWindow(3)
	w.ToggleWindowStash() // 3 stashed
	w.ToggleStashVisible()
	w.SetFullscreen(3, true) // waits
	w.FocusID(1)
	w.SetFullscreen(1, true)
	w.FocusID(2) // 1 scrolled off: it no longer covers
	if w.cover() != 0 {
		t.Fatal("tile still covers")
	}
	w.ToggleStashVisible()
	if w.fullscreen != 3 || w.hiddenFullscreen != 0 {
		t.Fatalf("fullscreen %d pending %d", w.fullscreen, w.hiddenFullscreen)
	}
	if id, _ := w.Focused(); id != 3 {
		t.Fatalf("focused %d", id)
	}
}

// A removed named workspace hands its stash to the active one; with no
// tiles there, the stash takes the focus.
func TestStashFromRemovedNamedWorkspace(t *testing.T) {
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
}

// stash.width sets the selected window's width; the neighbors sit
// stash.gap beside it, in what the margins show.
func TestStashWidth(t *testing.T) {
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
}
