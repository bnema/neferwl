package core

import (
	"strconv"
	"strings"
)

type Action string

const (
	ActionSpawnTerminal    Action = "spawn-terminal"
	ActionFocusColumnLeft  Action = "focus-column-left"
	ActionFocusColumnRight Action = "focus-column-right"
	ActionFocusWindowUp    Action = "focus-window-up"
	ActionFocusWindowDown  Action = "focus-window-down"
	ActionMoveColumnLeft   Action = "move-column-left"
	ActionMoveColumnRight  Action = "move-column-right"
	ActionCycleColumnWidth Action = "cycle-column-width"
	ActionMaximizeColumn   Action = "maximize-column"
	ActionToggleFullscreen Action = "toggle-fullscreen"
	ActionCloseWindow      Action = "close-window"
	ActionQuit             Action = "quit"
	// Workspaces stack vertically; up/down stop at the ends.
	ActionFocusWorkspaceUp   Action = "focus-workspace-up"
	ActionFocusWorkspaceDown Action = "focus-workspace-down"
	// Move the focused column, or only the focused window, one workspace
	// up or down.
	ActionMoveColumnToWorkspaceUp   Action = "move-column-to-workspace-up"
	ActionMoveColumnToWorkspaceDown Action = "move-column-to-workspace-down"
	ActionMoveWindowToWorkspaceUp   Action = "move-window-to-workspace-up"
	ActionMoveWindowToWorkspaceDown Action = "move-window-to-workspace-down"
	// Monitors are ordered left to right (ADR 011).
	ActionFocusMonitorLeft   Action = "focus-monitor-left"
	ActionFocusMonitorRight  Action = "focus-monitor-right"
	ActionMoveWorkspaceLeft  Action = "move-workspace-to-monitor-left"
	ActionMoveWorkspaceRight Action = "move-workspace-to-monitor-right"
	// Scale steps through the clean scales of the output (see CleanScales).
	ActionScaleUp   Action = "scale-up"
	ActionScaleDown Action = "scale-down"
	// Consume or expel the focused window (ADR 016).
	ActionConsumeOrExpelLeft  Action = "consume-or-expel-window-left"
	ActionConsumeOrExpelRight Action = "consume-or-expel-window-right"
)

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
	dir := 0
	switch a {
	case ActionFocusMonitorLeft, ActionMoveWorkspaceLeft:
		dir = -1
	case ActionFocusMonitorRight, ActionMoveWorkspaceRight:
		dir = 1
	}
	switch a {
	case ActionFocusMonitorLeft, ActionFocusMonitorRight:
		if i := c.neighbor(dir); i >= 0 {
			c.focusScreen = i
		}
		return Effect{}
	case ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight:
		c.moveWorkspace(dir)
		return Effect{}
	case ActionFocusColumnLeft, ActionFocusColumnRight:
		w := c.cur().mon.Current()
		edge := len(w.Columns) == 0 || (a == ActionFocusColumnLeft && w.Focus == 0) || (a == ActionFocusColumnRight && w.Focus == len(w.Columns)-1)
		if a == ActionFocusColumnLeft {
			dir = -1
		} else {
			dir = 1
		}
		if i := c.neighbor(dir); edge && i >= 0 {
			c.focusScreen = i
			return Effect{}
		}
	case ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight:
		dir = 1
		if a == ActionConsumeOrExpelLeft {
			dir = -1
		}
		from := c.cur()
		if from.mon.Current().ConsumeOrExpel(dir) {
			from.mon.normalize()
			return Effect{}
		}
		// Full fixed workspace, stacked edge column: one hop to the
		// neighbor monitor, if any; focus follows the window.
		i := c.neighbor(dir)
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
		dir = 1
		if a == ActionMoveColumnLeft {
			dir = -1
		}
		edge := (dir < 0 && w.Focus == 0) || (dir > 0 && w.Focus == len(w.Columns)-1)
		if i := c.neighbor(dir); edge && i >= 0 {
			col, ok := w.takeColumn()
			if !ok {
				return Effect{}
			}
			from := c.cur()
			c.focusScreen = i
			to := c.cur().mon.Current()
			at := 0
			if dir < 0 {
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
	if n, op, ok := WorkspaceArg(a); ok {
		if op == FocusWorkspace {
			m.FocusNumber(n)
		} else {
			m.MoveToWorkspace(n-1, op == MoveColumnToWorkspace)
		}
		return Effect{}
	}
	if name, ok := NamedArg(a); ok {
		m.ToggleNamed(name)
		return Effect{}
	}
	switch a {
	case ActionFocusWindowUp, ActionFocusWindowDown:
		// Past the top or bottom window of the column, move to the next workspace.
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
	case ActionMoveColumnToWorkspaceUp, ActionMoveWindowToWorkspaceUp:
		if m.shown == nil && m.Active > 0 {
			m.MoveToWorkspace(m.Active-1, a == ActionMoveColumnToWorkspaceUp)
		}
		return Effect{}
	case ActionMoveColumnToWorkspaceDown, ActionMoveWindowToWorkspaceDown:
		if m.shown == nil {
			m.MoveToWorkspace(m.Active+1, a == ActionMoveColumnToWorkspaceDown)
		}
		return Effect{}
	}
	return m.Current().Apply(a)
}

// Apply runs a bind action that only touches this workspace.
func (w *Workspace) Apply(a Action) Effect {
	if argv, ok := SpawnArgv(a); ok {
		return Effect{Spawn: true, Argv: argv}
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
	case ActionCycleColumnWidth:
		w.CycleWidth()
	case ActionMaximizeColumn:
		w.ToggleFullWidth()
	case ActionToggleFullscreen:
		// In place only: binds go through Monitor.Apply, which gives a
		// fixed-overflow fullscreen its own workspace.
		w.ToggleFullscreen()
	case ActionCloseWindow:
		id, _ := w.Focused()
		return Effect{Close: id}
	case ActionQuit:
		return Effect{Quit: true}
	}
	return Effect{}
}
