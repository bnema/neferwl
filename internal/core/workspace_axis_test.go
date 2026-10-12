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

// An occupied workspace wins over a monitor on that side, so the opposite
// key brings focus back; the monitor wins over the trailing empty
// workspace, so it stays reachable.
func TestCascadeFocusColumnStepsWorkspaceBeforeMonitor(t *testing.T) {
	c, m := axisCore(t, true, OverflowCascade, 3)
	m.Current().FocusID(23)
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 2)
	if got := c.cur().name(); got != "M" {
		t.Fatalf("focused screen %q, want M", got)
	}
	// Back with the opposite key, from the first column of workspace 3.
	m.Current().FocusID(31)
	c.applyAction(ActionFocusColumnLeft)
	wantActive(t, m, 1)
	if got := c.cur().name(); got != "M" {
		t.Fatalf("focused screen %q, want M", got)
	}
	// Past the first workspace: the monitor on the left.
	m.Focus(0)
	m.Current().FocusID(11)
	c.applyAction(ActionFocusColumnLeft)
	wantFocus(t, c, "L", 0)
	wantActive(t, m, 0)
	// From the last occupied workspace: the monitor on the right, not the
	// trailing empty workspace.
	c.focusScreen = 1
	m.Focus(2)
	m.Current().FocusID(35)
	c.applyAction(ActionFocusColumnRight)
	wantFocus(t, c, "R", 0)
	wantActive(t, m, 2)
}

// With no monitor on that side, the trailing empty workspace is the last
// stop, and left comes back from it.
func TestCascadeFocusColumnReachesTrailingWorkspaceWithoutMonitor(t *testing.T) {
	c, m := axisCore(t, false, OverflowCascade, 3)
	m.Focus(2)
	m.Current().FocusID(35)
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 3)
	if !m.Current().empty() {
		t.Fatal("want the trailing empty workspace")
	}
	c.applyAction(ActionFocusColumnLeft)
	wantActive(t, m, 2)
}

// The live regression: one monitor on the left only. Right steps to the
// next workspace, and left from its empty band comes back to the previous
// workspace instead of leaving for that monitor.
func TestCascadeStepRightThenLeftComesBack(t *testing.T) {
	c := layoutCore(1, tile{"L", 0, 0, 900, 600}, tile{"M", 900, 0, 900, 600})
	m := c.cur().mon
	m.SetOverflow(OverflowCascade)
	m.SetMaxColumns(3)
	m.AddWindow(1)
	m.AddWindow(5)
	c.applyAction(ActionFocusColumnRight)
	wantActive(t, m, 1)
	c.applyAction(ActionFocusColumnLeft)
	wantFocus(t, c, "M", 5)
	wantActive(t, m, 0)
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

	// A lone native float on its own workspace, past the trailing one.
	for _, tc := range []struct {
		a    Action
		want int
	}{{ActionFocusColumnLeft, 2}, {ActionFocusColumnRight, 4}} {
		c, m = axisCore(t, false, OverflowCascade, 3)
		m.Focus(3)
		m.AddFloating(9, 100, 100)
		if w := m.Current(); len(w.Columns) != 0 || !w.floatFocus {
			t.Fatalf("columns %d floatFocus %v, want a lone focused float", len(w.Columns), w.floatFocus)
		}
		c.applyAction(tc.a)
		wantActive(t, m, tc.want)
	}
}
