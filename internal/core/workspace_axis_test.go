package core

import "testing"

// axisCore is a cascade core whose monitor "M" has three workspaces of five
// windows (ids 11-15, 21-25, 31-35; bands of three) and a trailing empty
// one. Workspace 2 is active. With sides, monitors L and R flank M.
func axisCore(t *testing.T, sides bool, overflow Overflow, maxCols int) (*Core, *Monitor) {
	t.Helper()
	c, _ := cascadeMovesCore(t, 0, maxCols, sides)
	m := c.cur().mon
	m.SetOverflow(overflow)
	m.SetMaxColumns(maxCols)
	for ws := range 3 {
		m.Focus(ws)
		for i := 1; i <= 5; i++ {
			m.AddWindow(WindowID((ws+1)*10 + i))
		}
	}
	m.Focus(1)
	return c, m
}

func wantActive(t *testing.T, m *Monitor, active int) {
	t.Helper()
	if m.Active != active {
		t.Fatalf("active workspace %d, want %d", m.Active, active)
	}
}

// AC1: past the band edge with no monitor, left/right step the workspace.
func TestCascadeFocusColumnStepsWorkspaceAtBandEdge(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	m.Current().FocusID(23) // last column of band 1, not the last band
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 2)
	m.Focus(1)
	m.Current().FocusID(24) // first column of band 2
	c.applyAction(ActionFocusColumnLeft)
	wantActive(t, m, 0)
	// The first workspace stays put at the start.
	m.Current().FocusID(11)
	c.applyAction(ActionFocusColumnLeft)
	wantActive(t, m, 0)
	// Inside the band, focus moves and the workspace stays.
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 0)
	if id, _ := m.Current().Focused(); id != 12 {
		t.Fatalf("focused %d, want 12", id)
	}
}

// AC2: a monitor on that side wins over the workspace step.
func TestCascadeFocusColumnPrefersNeighborMonitor(t *testing.T) {
	c, m := axisCore(t, true, OverflowCascade, 3)
	m.Current().FocusID(23)
	c.applyAction(ActionFocusColumnRight)
	wantFocus(t, c, "R", 0)
	wantActive(t, m, 1)
	c.focusScreen = 1
	m.Current().FocusID(24)
	c.applyAction(ActionFocusColumnLeft)
	wantFocus(t, c, "L", 0)
	wantActive(t, m, 1)
}

// AC3: up/down cross bands and stop at the last, with no workspace change.
func TestCascadeFocusWindowDownStaysOnWorkspace(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	m.Current().FocusID(22)
	c.applyAction(ActionFocusWindowDown)
	wantFocus(t, c, "M", 25)
	wantActive(t, m, 1)
	c.applyAction(ActionFocusWindowDown)
	wantFocus(t, c, "M", 25)
	wantActive(t, m, 1)
}

// AC4: scroll and fixed switch workspace with up/down at the column
// bottom; left/right at the edge do nothing without a monitor.
func TestScrollFixedWorkspaceAxisIsVertical(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(overflow), func(t *testing.T) {
			c, m := axisCore(t, false, overflow, 3)
			m.Current().FocusID(25)
			c.applyAction(ActionFocusColumnRight)
			wantActive(t, m, 1)
			wantFocus(t, c, "M", 25)
			c.applyAction(ActionFocusColumnLeft)
			c.applyAction(ActionFocusColumnLeft)
			c.applyAction(ActionFocusColumnLeft)
			c.applyAction(ActionFocusColumnLeft)
			c.applyAction(ActionFocusColumnLeft)
			wantActive(t, m, 1)
			c.applyAction(ActionFocusWindowDown)
			wantActive(t, m, 2)
			m.Focus(1)
			m.Current().FocusID(21) // top of the first column
			c.applyAction(ActionFocusWindowUp)
			wantActive(t, m, 0)
		})
	}
}

// AC5: a shown named workspace is outside the workspace list.
func TestCascadeNamedWorkspaceDoesNotStep(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	m.SetNamed([]NamedWorkspace{{Name: "n"}})
	m.ToggleNamed("n")
	shown := m.shown
	if shown == nil {
		t.Fatal("named workspace not shown")
	}
	shown.Overflow = OverflowCascade
	shown.SetMaxColumns(3)
	for id := WindowID(41); id <= 45; id++ {
		m.AddWindow(id)
	}
	shown.FocusID(43)
	c.applyAction(ActionFocusColumnRight)
	if m.shown != shown || m.Active != 1 {
		t.Fatalf("shown %p want %p, active %d", m.shown, shown, m.Active)
	}
	wantFocus(t, c, "M", 43)
	shown.FocusID(41)
	c.applyAction(ActionFocusColumnLeft)
	if m.shown != shown || m.Active != 1 {
		t.Fatalf("shown %p want %p, active %d", m.shown, shown, m.Active)
	}
	wantFocus(t, c, "M", 41)
}

// AC6: a covering fullscreen tile at the band edge steps the workspace and
// stays fullscreen.
func TestCascadeFullscreenBandEdgeStepsWorkspace(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	w := m.Current()
	w.FocusID(23)
	w.ToggleFullscreen()
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 2)
	if w.fullscreen != 23 {
		t.Fatalf("fullscreen %d, want 23", w.fullscreen)
	}
}

// AC7: a lone native float, or an empty cascade workspace, is at the edge on
// both sides.
func TestCascadeLoneFloatAndEmptyWorkspaceStepWorkspace(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	m.Focus(3) // the trailing empty workspace
	if !m.Current().empty() {
		t.Fatal("workspace 4 not empty")
	}
	c.applyAction(ActionFocusColumnLeft)
	wantActive(t, m, 2)

	c, m = axisCore(t, false, OverflowCascade, 3)
	m.Focus(0)
	m.Current().AddFloating(9, 100, 100)
	for _, tc := range []struct {
		a    Action
		want int
	}{{ActionFocusColumnLeft, 0}, {ActionFocusColumnRight, 1}} {
		m.Focus(0)
		w := m.Current()
		w.Columns = nil // a lone float: no columns under it
		w.floatFocus = true
		c.applyAction(tc.a)
		wantActive(t, m, tc.want)
	}
}
