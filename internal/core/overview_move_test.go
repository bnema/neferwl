package core

import (
	"slices"
	"testing"
)

// Move-to-workspace binds move the selected preview, not the focus the
// overview opened on, and keep the overview open.
func TestOverviewMove(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		follow bool
		// row is the workspace index current after the move.
		row int
	}{
		{"column down", ActionMoveColumnToWorkspaceDown, false, 0},
		{"window down", ActionMoveWindowToWorkspaceDown, false, 0},
		{"column numbered", "move-column-to-workspace 2", false, 0},
		{"window numbered", "move-window-to-workspace 2", false, 0},
		{"follows", ActionMoveColumnToWorkspaceDown, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := overviewMonitor()
			m.SetFollowMove(tt.follow)
			m.Workspaces[0].FocusID(3)
			m.ToggleOverview()
			m.OverviewMove(-1, 0)
			if m.overviewTarget() != 2 {
				t.Fatalf("selection %d, want 2", m.overviewTarget())
			}
			m.Apply(tt.action)
			if !slices.Equal(m.Workspaces[0].windows(), []WindowID{1, 3}) || !slices.Contains(m.Workspaces[1].windows(), 2) {
				t.Fatalf("window 2 not moved: %v", windows(m))
			}
			if !m.ov.open || m.Current() != m.Workspaces[tt.row] {
				t.Fatalf("open %v row %d", m.ov.open, m.Active)
			}
			if tt.follow && m.overviewTarget() != 2 {
				t.Fatalf("selection %d did not follow", m.overviewTarget())
			}
			m.CancelOverview()
			if !slices.Contains(m.Workspaces[1].windows(), 2) {
				t.Fatal("escape undid the move")
			}
			if id, _ := m.Workspaces[0].Focused(); m.Current() != m.Workspaces[0] || id != 3 {
				t.Fatalf("workspace %d focus %d", m.Active, id)
			}
		})
	}
}

// The window variant moves only the selected window of a stacked column.
func TestOverviewMoveWindowFromStack(t *testing.T) {
	m := overviewMonitor()
	w := m.Current()
	w.FocusID(2)
	w.ConsumeOrExpel(-1)
	if len(w.Columns) != 2 {
		t.Fatalf("columns %v", windows(m))
	}
	w.FocusID(3)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.overviewTarget() != 2 {
		t.Fatalf("selection %d", m.overviewTarget())
	}
	m.Apply(ActionMoveWindowToWorkspaceDown)
	if !slices.Equal(w.windows(), []WindowID{1, 3}) || !slices.Contains(m.Workspaces[1].windows(), 2) {
		t.Fatalf("windows %v", windows(m))
	}
}

// The selected column moves even when a covering float is the row's focus.
func TestOverviewMoveSelectedUnderFloat(t *testing.T) {
	m := stackMonitor()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	id := m.overviewTarget()
	if id == 0 || id == 9 {
		t.Fatalf("selection %d", id)
	}
	m.Apply(ActionMoveColumnToWorkspaceDown)
	if !slices.Contains(m.Workspaces[1].windows(), id) || m.Workspaces[1].isFloat(9) {
		t.Fatalf("moved the wrong window: %v floats %v", windows(m), m.Workspaces[0].Floats)
	}
	// The provisional stack front went with the moved column.
	if m.ov.row != nil || m.ov.selected != 0 {
		t.Fatalf("stale selection: row %v selected %d", m.ov.row != nil, m.ov.selected)
	}
}

// A bind with nothing to move or nowhere to go leaves the overview as is.
func TestOverviewMoveNoop(t *testing.T) {
	tests := []struct {
		name   string
		setup  func() *Monitor
		action Action
	}{
		{"stash card", func() *Monitor {
			m := pileMonitor()
			m.ToggleOverview()
			m.OverviewMove(-1, 0)
			if m.cardAt(m.Current()) < 0 {
				t.Fatal("no stash card selected")
			}
			return m
		}, ActionMoveColumnToWorkspaceDown},
		{"hidden small float", func() *Monitor {
			m := newMonitor("", "")
			m.SetOutput(300, 200)
			m.AddFloating(9, 50, 50)
			m.ToggleOverview()
			return m
		}, ActionMoveWindowToWorkspaceDown},
		{"up from first", func() *Monitor {
			m := overviewMonitor()
			m.ToggleOverview()
			m.OverviewMove(-1, 0)
			return m
		}, ActionMoveColumnToWorkspaceUp},
		{"to its own row", func() *Monitor {
			m := overviewMonitor()
			m.ToggleOverview()
			m.OverviewMove(-1, 0)
			return m
		}, "move-column-to-workspace 1"},
		{"down from named", func() *Monitor {
			m := namedOverviewMonitor()
			m.ToggleNamed("game")
			m.ToggleOverview()
			return m
		}, ActionMoveColumnToWorkspaceDown},
		{"fixed maximize kept", func() *Monitor {
			m := overviewMonitor()
			w := m.Current()
			w.Overflow = OverflowFixed
			w.FocusID(3)
			w.ToggleFullWidth()
			m.ToggleOverview()
			m.OverviewMove(0, -1)
			return m
		}, ActionMoveColumnToWorkspaceUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup()
			before, ws := m.Layout(), windows(m)
			m.Apply(tt.action)
			if !slices.Equal(before, m.Layout()) || !slices.EqualFunc(ws, windows(m), slices.Equal) {
				t.Fatalf("changed: %v -> %v", ws, windows(m))
			}
		})
	}
}

// A browsed destination row keeps the moved window focused on close.
func TestOverviewMoveDestinationKeepsFocus(t *testing.T) {
	m := overviewMonitor()
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.OverviewMove(0, -1)
	m.OverviewMove(-1, 0)
	m.Apply(ActionMoveColumnToWorkspaceDown)
	m.ToggleOverview()
	if id, _ := m.Workspaces[1].Focused(); id != 2 {
		t.Fatalf("destination focus %d, want 2", id)
	}
}

// Closing after a move drops the moved window from the restored maximize
// history.
func TestOverviewMoveDropsMaximizeHistory(t *testing.T) {
	m := overviewMonitor()
	w := m.Current()
	w.Overflow = OverflowFixed
	w.FocusID(3)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.Apply(ActionMoveColumnToWorkspaceDown)
	if w.has(3) {
		t.Fatal("3 not moved")
	}
	m.CancelOverview()
	if slices.Contains(w.maximized, 3) {
		t.Fatalf("maximized %v keeps a moved window", w.maximized)
	}
}
