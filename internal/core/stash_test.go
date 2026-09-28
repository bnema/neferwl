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
		w.ToggleWindowFloating()
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
		to.ToggleWindowFloating()
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
	w.ToggleFloatingVisible()
	m.take(w)
	dst := newMonitor("", "")
	dst.SetOutput(200, 100)
	dst.adopt(w, false, 0)
	if !slices.Equal(stashIDs(w), []WindowID{2, 3}) || w.stashAt != 0 || !w.stashHidden {
		t.Fatalf("stash %v at %d hidden %v", stashIDs(w), w.stashAt, w.stashHidden)
	}
	w.ToggleFloatingVisible()
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
