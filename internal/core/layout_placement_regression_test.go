package core

import (
	"testing"
	"testing/synctest"
)

// A window arriving under a covering fullscreen window waits at the end of a
// cascade workspace, not after the focused column as in scroll.

// Invariant: a maximized cascade column fills its band, whatever its cells or
// manual width. It always forms a band of its own, so the band walk gives its
// overview tile the full usable width from the left gap.

// A workspace made from the monitor template must not share its layout state
// with the template or with another workspace: the cascade bands of one would
// leak into the other. The template builds state when SetMaxColumns settles
// it, and every workspace made afterwards used to alias that state.
func TestNewWorkspaceDoesNotShareLayoutState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(1920, 1080)
		m.SetUsable(Rect{W: 1920, H: 1080})
		m.SetGaps(8)
		m.SetOverflow(OverflowCascade)
		m.SetMaxColumns(2)
		m.AddWindow(1)
		m.Apply(ActionFocusWorkspaceNext) // a second workspace, made from the template
		m.AddWindow(2)
		m.AddWindow(3)
		m.AddWindow(4)
		// The column leaves for a third workspace, made from the template again.
		m.Apply(ActionMoveColumnToWorkspaceNext)

		w := m.Workspaces[m.Active]
		if len(w.Columns) != 2 {
			t.Fatalf("columns = %d, want 2", len(w.Columns))
		}
		// Two columns fit the two-column capacity: one band, side by side.
		r := modelColumnRectsAt(w, nil, false, 0)
		if r[0].Y != r[1].Y || r[0].X == r[1].X {
			t.Fatalf("columns are not in one band: %v", r)
		}
		for i, a := range m.Workspaces {
			for j, b := range m.Workspaces {
				if i < j && a.Snapshot().State != nil && a.Snapshot().State == b.Snapshot().State {
					t.Fatalf("workspaces %d and %d share layout state", i, j)
				}
			}
			if a.Snapshot().State != nil && a.Snapshot().State == m.template.Snapshot().State {
				t.Fatalf("workspace %d shares the template's layout state", i)
			}
		}

	})
}
