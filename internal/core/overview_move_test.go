package core

import (
	"slices"
	"testing"
)

// moveFromOverview opens the overview on overviewMonitor, selects
// window 2 and runs a.
func moveFromOverview(t *testing.T, follow bool, a Action) *Monitor {
	t.Helper()
	m := overviewMonitor()
	m.SetFollowMove(follow)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.overviewTarget() != 2 {
		t.Fatalf("selection %d, want 2", m.overviewTarget())
	}
	m.Apply(a)
	if slices.Contains(m.Workspaces[0].windows(), 2) || !slices.Contains(m.Workspaces[1].windows(), 2) {
		t.Fatalf("window 2 not moved: %v", windows(m))
	}
	if !m.ov.open {
		t.Fatal("overview closed")
	}
	return m
}

func TestOverviewMoveColumnDown(t *testing.T) {
	m := moveFromOverview(t, false, ActionMoveColumnToWorkspaceDown)
	if m.Current() != m.Workspaces[0] {
		t.Fatal("selection left the row")
	}
	if id := m.overviewTarget(); id == 0 || !slices.Contains(m.Workspaces[0].windows(), id) {
		t.Fatalf("selection %d not on workspace 1", id)
	}
}

func TestOverviewMoveNumbered(t *testing.T) {
	m := moveFromOverview(t, false, "move-column-to-workspace 2")
	if m.Current() != m.Workspaces[0] {
		t.Fatal("selection left the row")
	}
}

func TestOverviewMoveFollows(t *testing.T) {
	m := moveFromOverview(t, true, ActionMoveColumnToWorkspaceDown)
	if m.Current() != m.Workspaces[1] || m.overviewTarget() != 2 {
		t.Fatalf("selection did not follow: workspace %d target %d", m.Active, m.overviewTarget())
	}
}

func TestOverviewMoveEscapeKeepsMove(t *testing.T) {
	m := moveFromOverview(t, false, ActionMoveColumnToWorkspaceDown)
	m.CancelOverview()
	if !slices.Contains(m.Workspaces[1].windows(), 2) {
		t.Fatal("escape undid the move")
	}
	if id, _ := m.Current().Focused(); m.Current() != m.Workspaces[0] || id != 3 {
		t.Fatalf("workspace %d focus %d", m.Active, id)
	}
}

func TestOverviewMoveStashCardNoop(t *testing.T) {
	m := pileMonitor()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if m.cardAt(m.Current()) < 0 {
		t.Fatal("no stash card selected")
	}
	before := m.Layout()
	m.Apply(ActionMoveColumnToWorkspaceDown)
	if after := m.Layout(); !slices.Equal(before, after) {
		t.Fatalf("layout changed: %+v -> %+v", before, after)
	}
}
