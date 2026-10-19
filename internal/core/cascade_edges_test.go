package core

import "testing"

// With no monitor on that side, move-column-left/right at a cascade band
// edge does nothing: the column never crosses into the adjacent band.
// Bands {1 2 3} {4 5}.
func TestCascadeMoveColumnAtBandEdgeDoesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		from   WindowID
		action Action
	}{
		{"right from band end", 3, ActionMoveColumnRight},
		{"left from band start", 4, ActionMoveColumnLeft},
		{"left from first column", 1, ActionMoveColumnLeft},
		{"right from last column", 5, ActionMoveColumnRight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 5, 3, false)
			w.FocusID(tc.from)
			c.applyAction(tc.action)
			wantColumns(t, w, 1, 2, 3, 4, 5)
			wantFocus(t, c, "M", tc.from)
			wantActive(t, c.cur().mon, 0)
		})
	}
}

// Inside a band the column still swaps with its neighbor.
func TestCascadeMoveColumnInsideBandSwaps(t *testing.T) {
	c, w := cascadeMovesCore(t, 5, 3, false)
	w.FocusID(2)
	c.applyAction(ActionMoveColumnRight)
	wantColumns(t, w, 1, 3, 2, 4, 5)
	wantFocus(t, c, "M", 2)
	c.applyAction(ActionMoveColumnRight)
	wantColumns(t, w, 1, 3, 2, 4, 5)
	c.applyAction(ActionMoveColumnLeft)
	c.applyAction(ActionMoveColumnLeft)
	wantColumns(t, w, 2, 1, 3, 4, 5)
	c.applyAction(ActionMoveColumnLeft)
	wantColumns(t, w, 2, 1, 3, 4, 5)
}

// An occupied workspace wins over the neighbor monitor at the band edge.
func TestCascadeFocusColumnEdgePrefersWorkspaceOverMonitor(t *testing.T) {
	c := layoutCore(0, tile{"L", 0, 0, 900, 600}, tile{"R", 900, 0, 900, 600})
	m := c.cur().mon
	m.SetOverflow(OverflowCascade)
	m.SetMaxColumns(3)
	m.AddWindow(1)
	m.AddWindow(2)
	m.Focus(1)
	m.AddWindow(3) // an occupied workspace to the right on L
	m.Focus(0)
	m.normalize()
	m.Current().FocusID(2)
	c.applyAction(ActionFocusColumnRight)
	wantFocus(t, c, "L", 3)
	wantActive(t, m, 1)
}

// With no monitor on that side, the occupied neighbor workspace is next.
func TestCascadeFocusColumnEdgeSwitchesToOccupiedWorkspace(t *testing.T) {
	c := layoutCore(0, tile{"M", 0, 0, 900, 600})
	m := c.cur().mon
	m.SetOverflow(OverflowCascade)
	m.SetMaxColumns(3)
	m.AddWindow(1)
	m.AddWindow(2)
	m.Focus(1)
	m.AddWindow(3)
	m.Focus(0)
	m.normalize()
	m.Current().FocusID(2)
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 1)
	wantFocus(t, c, "M", 3)
}
