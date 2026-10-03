package core

import (
	"strconv"
	"strings"
)

type Action string

const (
	ActionSpawnTerminal      Action = "spawn-terminal"
	ActionFocusColumnLeft    Action = "focus-column-left"
	ActionFocusColumnRight   Action = "focus-column-right"
	ActionFocusWindowUp      Action = "focus-window-up"
	ActionFocusWindowDown    Action = "focus-window-down"
	ActionMoveColumnLeft     Action = "move-column-left"
	ActionMoveColumnRight    Action = "move-column-right"
	ActionCycleColumnWidth   Action = "cycle-column-width"
	ActionMaximizeColumn     Action = "maximize-column"
	ActionToggleFullscreen   Action = "toggle-fullscreen"
	ActionToggleWindowStash  Action = "toggle-window-stash"
	ActionToggleStashVisible Action = "toggle-stash-visible"
	ActionToggleOverview     Action = "toggle-overview"
	ActionCloseWindow        Action = "close-window"
	ActionQuit               Action = "quit"
	// Workspaces stack vertically; up/down stop at the ends.
	ActionFocusWorkspaceUp   Action = "focus-workspace-up"
	ActionFocusWorkspaceDown Action = "focus-workspace-down"
	// Move the focused column, or only the focused window, one workspace
	// up or down.
	ActionMoveColumnToWorkspaceUp   Action = "move-column-to-workspace-up"
	ActionMoveColumnToWorkspaceDown Action = "move-column-to-workspace-down"
	ActionMoveWindowToWorkspaceUp   Action = "move-window-to-workspace-up"
	ActionMoveWindowToWorkspaceDown Action = "move-window-to-workspace-down"
	// Monitors act on the neighbor in that direction of the global layout
	// (ADR 011).
	ActionFocusMonitorLeft           Action = "focus-monitor-left"
	ActionFocusMonitorRight          Action = "focus-monitor-right"
	ActionFocusMonitorUp             Action = "focus-monitor-up"
	ActionFocusMonitorDown           Action = "focus-monitor-down"
	ActionMoveWorkspaceLeft          Action = "move-workspace-to-monitor-left"
	ActionMoveWorkspaceRight         Action = "move-workspace-to-monitor-right"
	ActionMoveWorkspaceToMonitorUp   Action = "move-workspace-to-monitor-up"
	ActionMoveWorkspaceToMonitorDown Action = "move-workspace-to-monitor-down"
	// Scale steps through the clean scales of the output (see CleanScales).
	ActionScaleUp   Action = "scale-up"
	ActionScaleDown Action = "scale-down"
	// Consume or expel the focused window (ADR 016).
	ActionConsumeOrExpelLeft  Action = "consume-or-expel-window-left"
	ActionConsumeOrExpelRight Action = "consume-or-expel-window-right"
	// Move the focused window up or down inside its column.
	ActionMoveWindowUp   Action = "move-window-up"
	ActionMoveWindowDown Action = "move-window-down"
	// Swap the active numbered workspace with its neighbor.
	ActionMoveWorkspaceUp   Action = "move-workspace-up"
	ActionMoveWorkspaceDown Action = "move-workspace-down"
	// Turn the focused tile into a free floating window, or back.
	ActionToggleFloating Action = "toggle-floating"
)

// monitorDirections maps the focus-monitor and move-workspace-to-monitor
// actions to the direction of the neighbor screen they target.
var monitorDirections = map[Action]direction{
	ActionFocusMonitorLeft: dirLeft, ActionFocusMonitorRight: dirRight,
	ActionFocusMonitorUp: dirUp, ActionFocusMonitorDown: dirDown,
	ActionMoveWorkspaceLeft: dirLeft, ActionMoveWorkspaceRight: dirRight,
	ActionMoveWorkspaceToMonitorUp: dirUp, ActionMoveWorkspaceToMonitorDown: dirDown,
}

// Resize axes of ResizeArg.
const (
	ResizeWidth  = 0
	ResizeHeight = 1
)

var resizePrefixes = [...]string{
	ResizeWidth:  "set-column-width ",
	ResizeHeight: "set-window-height ",
}

// ResizeArg parses "set-column-width +N%" (axis ResizeWidth) and
// "set-window-height -N%" (axis ResizeHeight); N is 1 to 100 and the sign
// is required.
func ResizeArg(a Action) (axis, pct int, ok bool) {
	for i, prefix := range resizePrefixes {
		rest, found := strings.CutPrefix(string(a), prefix)
		if !found {
			continue
		}
		if p, ok := parseResizeStep(strings.TrimSpace(rest)); ok {
			return i, p, true
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// parseResizeStep parses "+N%" or "-N%" with N from 1 to 100.
func parseResizeStep(s string) (int, bool) {
	if len(s) < 3 || (s[0] != '+' && s[0] != '-') || s[len(s)-1] != '%' {
		return 0, false
	}
	n, err := strconv.Atoi(s[1 : len(s)-1])
	if err != nil || n < 1 || n > 100 || s[1] == '+' || s[1] == '-' {
		return 0, false
	}
	if s[0] == '-' {
		n = -n
	}
	return n, true
}

// WorkspaceOp is what a numbered workspace action does.
type WorkspaceOp int

const (
	FocusWorkspace WorkspaceOp = iota
	MoveColumnToWorkspace
	MoveWindowToWorkspace
)

var workspacePrefixes = [...]string{
	FocusWorkspace:        "focus-workspace ",
	MoveColumnToWorkspace: "move-column-to-workspace ",
	MoveWindowToWorkspace: "move-window-to-workspace ",
}

// WorkspaceArg parses "focus-workspace N", "move-column-to-workspace N" and
// "move-window-to-workspace N" (N from 1).
func WorkspaceArg(a Action) (n int, op WorkspaceOp, ok bool) {
	for i, prefix := range workspacePrefixes {
		rest, found := strings.CutPrefix(string(a), prefix)
		if !found {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n < 1 || n > 99 {
			return 0, 0, false
		}
		return n, WorkspaceOp(i), true
	}
	return 0, 0, false
}

// namedPrefix toggles a named workspace: "workspace dev".
const namedPrefix = "workspace "

// NamedArg returns the name of a "workspace <name>" action.
func NamedArg(a Action) (string, bool) {
	rest, ok := strings.CutPrefix(string(a), namedPrefix)
	name := strings.TrimSpace(rest)
	return name, ok && name != "" && !strings.ContainsAny(name, " \t")
}

// spawnPrefix starts a bind action that runs a command: "spawn fuzzel --flag".
// Arguments are split on whitespace; there is no shell (use "spawn sh -c ...").
const spawnPrefix = "spawn "

// SpawnArgv returns the command of a "spawn <cmd>" action.
func SpawnArgv(a Action) ([]string, bool) {
	rest, ok := strings.CutPrefix(string(a), spawnPrefix)
	argv := strings.Fields(rest)
	return argv, ok && len(argv) > 0
}

type Effect struct {
	Spawn bool
	Argv  []string // command to run; nil means the configured terminal
	Close WindowID
	Quit  bool
}

// applyAction runs a bind action. Monitor actions change the focused
// screen; column focus past the edge column moves to the neighbor screen;
// everything else applies to the focused screen's monitor.
func (c *Core) applyAction(a Action) Effect {
	if e, ok := c.cur().mon.overviewAction(a); ok {
		return e
	}
	// A bind acts on the window on screen: under a covering fullscreen
	// window, that is the one Focused reports.
	c.cur().mon.Current().focusCover()
	// Before the edge moves below: a free float moves on its screen.
	if c.cur().mon.Current().freeAction(a) {
		return Effect{}
	}
	if name, ok := NamedArg(a); ok && c.bringNamed(name) {
		return Effect{}
	}
	switch a {
	case ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown:
		if i := c.neighbor(monitorDirections[a]); i >= 0 {
			c.focusScreen = i
		}
		return Effect{}
	case ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown:
		c.moveWorkspace(monitorDirections[a])
		return Effect{}
	case ActionFocusColumnLeft, ActionFocusColumnRight:
		w := c.cur().mon.Current()
		dir, d := 1, dirRight
		if a == ActionFocusColumnLeft {
			dir, d = -1, dirLeft
		}
		// A covering fullscreen window is left for the window on that side;
		// with none (the workspace or stash edge), the move goes to the
		// neighbor monitor and fullscreen stays.
		if w.pinned() {
			if !w.FocusColumn(dir) {
				if i := c.neighbor(d); i >= 0 {
					c.focusScreen = i
				}
			}
			return Effect{}
		}
		// The first move from a float stays here unless nothing is under it:
		// a native float leaves for the stash or the columns, and the stash
		// keeps the focus at its ends.
		edge := w.columnToward(dir) < 0 && (!w.onFloat() || w.floatFocus && !w.canLeaveFloat())
		if i := c.neighbor(d); edge && i >= 0 {
			c.focusScreen = i
			return Effect{}
		}
	case ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight:
		dir, d := 1, dirRight
		if a == ActionConsumeOrExpelLeft {
			dir, d = -1, dirLeft
		}
		from := c.cur()
		if from.mon.Current().ConsumeOrExpel(dir) {
			from.mon.normalize()
			return Effect{}
		}
		// Full fixed workspace, stacked edge column: one hop to the
		// neighbor monitor, if any; focus follows the window.
		i := c.neighbor(d)
		if i < 0 {
			return Effect{}
		}
		id, ok := from.mon.Current().takeWindow()
		if !ok {
			return Effect{}
		}
		c.focusScreen = i
		c.cur().mon.Current().expelTo(id, dir)
		from.mon.normalize()
		c.cur().mon.normalize()
		return Effect{}
	case ActionMoveColumnLeft, ActionMoveColumnRight:
		// Past the edge the column moves to the neighbor screen, on the
		// side facing this one, and focus follows it.
		w := c.cur().mon.Current()
		d := dirRight
		if a == ActionMoveColumnLeft {
			d = dirLeft
		}
		edge := (d == dirLeft && w.Focus == 0) || (d == dirRight && w.Focus == len(w.Columns)-1)
		if i := c.neighbor(d); edge && i >= 0 {
			col, ok := w.takeColumn()
			if !ok {
				return Effect{}
			}
			from := c.cur()
			c.focusScreen = i
			to := c.cur().mon.Current()
			at := 0
			if d == dirLeft {
				at = len(to.Columns)
			}
			to.receive(col, at)
			from.mon.normalize()
			c.cur().mon.normalize()
			return Effect{}
		}
	}
	return c.cur().mon.Apply(a)
}

// Apply runs a bind action on the monitor.
func (m *Monitor) Apply(a Action) Effect {
	if e, ok := m.overviewAction(a); ok {
		return e
	}
	if movesToWorkspace(a) {
		if i, column, ok := m.moveDest(a); ok {
			m.MoveToWorkspace(i, column)
		}
		return Effect{}
	}
	if n, _, ok := WorkspaceArg(a); ok {
		before := m.Current()
		m.FocusNumber(n)
		if m.ov.open && m.Current() != before {
			m.selectRow()
		}
		return Effect{}
	}
	if name, ok := NamedArg(a); ok {
		before := m.Current()
		m.ToggleNamed(name)
		if m.ov.open && m.Current() != before {
			m.selectRow()
		}
		return Effect{}
	}
	switch a {
	case ActionFocusWindowUp, ActionFocusWindowDown:
		// Past the column and any on-screen neighbor or demoted float,
		// move to the next workspace.
		dir := 1
		if a == ActionFocusWindowUp {
			dir = -1
		}
		// A hidden workspace is outside the vertical list.
		if !m.Current().FocusWindow(dir) && m.shown == nil {
			m.Focus(m.Active + dir)
		}
		return Effect{}
	case ActionToggleFullscreen:
		m.ToggleFullscreen()
		return Effect{}
	case ActionToggleWindowStash:
		m.Current().ToggleWindowStash()
		return Effect{}
	case ActionToggleStashVisible:
		m.Current().ToggleStashVisible()
		return Effect{}
	case ActionToggleOverview:
		m.ToggleOverview()
		return Effect{}
	case ActionMaximizeColumn:
		// A client that made itself fullscreen (Wine at monitor size) goes
		// back to its column width; the next press maximizes the column.
		if w := m.Current(); w.fullscreen != 0 {
			if id, ok := w.Focused(); ok && id == w.fullscreen {
				m.ToggleFullscreen()
				return Effect{}
			}
		}
	case ActionFocusWorkspaceUp, ActionFocusWorkspaceDown:
		if m.shown == nil && a == ActionFocusWorkspaceUp {
			m.Focus(m.Active - 1)
		} else if m.shown == nil {
			m.Focus(m.Active + 1)
		}
		return Effect{}
	case ActionMoveWorkspaceUp:
		m.MoveWorkspace(-1)
		return Effect{}
	case ActionMoveWorkspaceDown:
		m.MoveWorkspace(1)
		return Effect{}
	}
	return m.Current().Apply(a)
}

// movesToWorkspace reports a bind that moves a window to another workspace.
func movesToWorkspace(a Action) bool {
	if _, op, ok := WorkspaceArg(a); ok {
		return op != FocusWorkspace
	}
	switch a {
	case ActionMoveColumnToWorkspaceUp, ActionMoveColumnToWorkspaceDown,
		ActionMoveWindowToWorkspaceUp, ActionMoveWindowToWorkspaceDown:
		return true
	}
	return false
}

// moveDest resolves a move-to-workspace bind to the workspace index it
// moves to and whether the whole column goes; ok is false when it has
// nowhere to go. Up and down do nothing on a named workspace.
func (m *Monitor) moveDest(a Action) (i int, column, ok bool) {
	if n, op, isArg := WorkspaceArg(a); isArg {
		return min(max(n-1, 0), len(m.Workspaces)-1), op == MoveColumnToWorkspace, true
	}
	if m.shown != nil {
		return 0, false, false
	}
	switch a {
	case ActionMoveColumnToWorkspaceUp, ActionMoveWindowToWorkspaceUp:
		return m.Active - 1, a == ActionMoveColumnToWorkspaceUp, m.Active > 0
	case ActionMoveColumnToWorkspaceDown, ActionMoveWindowToWorkspaceDown:
		return min(m.Active+1, len(m.Workspaces)-1), a == ActionMoveColumnToWorkspaceDown, true
	}
	return 0, false, false
}

// Apply runs a bind action that only touches this workspace.
func (w *Workspace) Apply(a Action) Effect {
	if argv, ok := SpawnArgv(a); ok {
		return Effect{Spawn: true, Argv: argv}
	}
	if w.freeAction(a) {
		return Effect{}
	}
	if axis, pct, ok := ResizeArg(a); ok {
		if axis == ResizeWidth {
			w.ResizeColumn(pct)
		} else {
			w.ResizeRow(pct)
		}
		return Effect{}
	}
	switch a {
	case ActionSpawnTerminal:
		return Effect{Spawn: true}
	case ActionFocusColumnLeft:
		w.FocusColumn(-1)
	case ActionFocusColumnRight:
		w.FocusColumn(1)
	case ActionFocusWindowUp:
		w.FocusWindow(-1)
	case ActionFocusWindowDown:
		w.FocusWindow(1)
	case ActionMoveColumnLeft:
		w.MoveColumn(-1)
	case ActionMoveColumnRight:
		w.MoveColumn(1)
	case ActionMoveWindowUp:
		w.MoveWindow(-1)
	case ActionMoveWindowDown:
		w.MoveWindow(1)
	case ActionCycleColumnWidth:
		w.CycleWidth()
	case ActionMaximizeColumn:
		w.ToggleFullWidth()
	case ActionToggleFullscreen:
		w.ToggleFullscreen()
	case ActionToggleWindowStash:
		w.ToggleWindowStash()
	case ActionToggleStashVisible:
		w.ToggleStashVisible()
	case ActionToggleFloating:
		w.ToggleFloating()
	case ActionCloseWindow:
		id, _ := w.Focused()
		return Effect{Close: id}
	case ActionQuit:
		return Effect{Quit: true}
	}
	return Effect{}
}
