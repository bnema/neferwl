package core

import (
	"slices"
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/ports"
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
	// Workspaces stack vertically; prev/next stop at the ends.
	ActionFocusWorkspacePrev Action = "focus-workspace-prev"
	ActionFocusWorkspaceNext Action = "focus-workspace-next"
	// Move the focused column, or only the focused window, to the previous
	// or next workspace.
	ActionMoveColumnToWorkspacePrev Action = "move-column-to-workspace-prev"
	ActionMoveColumnToWorkspaceNext Action = "move-column-to-workspace-next"
	ActionMoveWindowToWorkspacePrev Action = "move-window-to-workspace-prev"
	ActionMoveWindowToWorkspaceNext Action = "move-window-to-workspace-next"
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
	ActionMoveWorkspacePrev Action = "move-workspace-prev"
	ActionMoveWorkspaceNext Action = "move-workspace-next"
	// Turn the focused tile into a free floating window, or back.
	ActionToggleFloating Action = "toggle-floating"
	// Switch to the column used before this one, further back while the
	// command key is held (switcher.go).
	ActionSwitchColumnNext Action = "switch-column-next"
	ActionSwitchColumnPrev Action = "switch-column-prev"
	// Show keyboard hint labels over the links and buttons of the focused
	// output (hint.go).
	ActionHint Action = "hint"
	// ActionOSDPin toggles the pinned window info overlay.
	ActionOSDPin Action = "osd-pin"
)

// monitorDirection returns the direction of the neighbor screen targeted by
// a focus-monitor or move-workspace-to-monitor action, 0 for other actions.
func monitorDirection(a Action) direction {
	switch a {
	case ActionFocusMonitorLeft, ActionMoveWorkspaceLeft:
		return dirLeft
	case ActionFocusMonitorRight, ActionMoveWorkspaceRight:
		return dirRight
	case ActionFocusMonitorUp, ActionMoveWorkspaceToMonitorUp:
		return dirUp
	case ActionFocusMonitorDown, ActionMoveWorkspaceToMonitorDown:
		return dirDown
	}
	return 0
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

// ResizeArg parses "set-column-width +N" (axis ResizeWidth) and
// "set-window-height -N%" (axis ResizeHeight). The sign is required and the
// unit depends on the axis: step is in cells for a width (N from 1 to 16,
// no %), in percent for a height (N from 1 to 100, with %).
func ResizeArg(a Action) (axis, step int, ok bool) {
	for i, prefix := range resizePrefixes {
		rest, found := strings.CutPrefix(string(a), prefix)
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		if i == ResizeWidth {
			if p, ok := parseResizeStep(rest, layout.MaxCells); ok {
				return i, p, true
			}
			return 0, 0, false
		}
		if num, found := strings.CutSuffix(rest, "%"); found {
			if p, ok := parseResizeStep(num, 100); ok {
				return i, p, true
			}
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// parseResizeStep parses "+N" or "-N" with N from 1 to limit.
func parseResizeStep(s string, limit int) (int, bool) {
	if len(s) < 2 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > limit || s[1] == '+' || s[1] == '-' {
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

// spawnPrefix starts a bind action that runs commands: "spawn fuzzel --flag".
// They are split by ports.SplitCommands; there is no shell.
const spawnPrefix = "spawn "

// SpawnCommands returns the commands of a "spawn <cmd>[; <cmd>...]" action.
func SpawnCommands(a Action) ([][]string, bool) { return prefixedCommands(a, spawnPrefix) }

// stashPrefix starts a bind action that runs commands and opens their
// windows in the stash: "stash firefox".
const stashPrefix = "stash "

// StashCommands returns the commands of a "stash <cmd>[; <cmd>...]" action.
func StashCommands(a Action) ([][]string, bool) { return prefixedCommands(a, stashPrefix) }

func prefixedCommands(a Action, prefix string) ([][]string, bool) {
	rest, ok := strings.CutPrefix(string(a), prefix)
	if !ok {
		return nil, false
	}
	commands := ports.SplitCommands(rest)
	return commands, len(commands) > 0
}

type Effect struct {
	Spawn    bool
	Commands [][]string // commands to run; nil means the configured terminal
	// Stash sends the spawned windows to the stash of the workspace the
	// action applied to.
	Stash bool
	Close WindowID
	Quit  bool
}

// applyAction runs a bind action. Focus past a content edge visits an occupied
// workspace, then a neighbor screen, then a trailing empty workspace. Vertical
// edges do this only with a vertical workspace axis; cascade stays within bands.
// Other actions apply to the focused screen's monitor unless explicitly moving
// between screens. Screen changes flash the focused window on the destination,
// and so does a switch to another workspace of the same screen.
func (c *Core) applyAction(a Action) Effect {
	if a == ActionOSDPin {
		c.osdApply(osdTogglePin)
		return Effect{}
	}
	if !c.flashOn() {
		return c.dispatchAction(a)
	}
	switch a {
	case ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveWindowUp, ActionMoveWindowDown,
		ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight, ActionToggleFloating,
		ActionToggleWindowStash, ActionToggleStashVisible, ActionToggleOverview,
		ActionFocusColumnLeft, ActionFocusColumnRight, ActionMoveColumnLeft, ActionMoveColumnRight,
		ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown,
		ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown:
	default:
		if !switchesWorkspace(a) {
			return c.dispatchAction(a)
		}
	}
	// Compare the action's visible result, not publication-time focus: pointer,
	// activation and mapping changes must never request a flash.
	beforeScreen := c.cur()
	beforeWS := beforeScreen.mon.Current()
	beforeID, _ := beforeScreen.mon.Focused()
	before := beforeScreen.mon.Layout()
	wasOverview := beforeScreen.mon.ov.open
	wasStashed := beforeWS.StashIndex(beforeID) >= 0
	wasShown := beforeWS.StashShown()
	defer func() {
		sc := c.cur()
		id, _ := sc.mon.Focused()
		if sc.mon.ov.open {
			return
		}
		if wasOverview {
			if a == ActionToggleOverview && id != 0 {
				c.requestFlash(flashWindow, id)
			}
			return
		}
		// Crossing outputs marks the destination’s focused window, if any.
		if sc != beforeScreen {
			if id != 0 {
				for _, p := range sc.mon.Layout() {
					if p.ID == id && sc.mon.viewport().shows(p) {
						c.requestFlash(flashWindow, id)
						break
					}
				}
			}
			return
		}
		// A switch to another workspace of this screen marks that
		// workspace's focused window once the slide settles. It is always a
		// window flash: the column-level border of the action that crossed
		// the edge would outline a column the user never navigated within.
		// Switches made inside the overview only move its selection; the
		// overview branches above have already returned for them.
		if sc.mon.Current() != beforeWS {
			if id != 0 {
				c.requestFlash(flashWindow, id)
			}
			return
		}
		level := flashWindow
		switch a {
		case ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveWindowUp, ActionMoveWindowDown,
			ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight, ActionToggleFloating:
		case ActionToggleWindowStash:
			if !wasStashed {
				return
			}
		case ActionToggleStashVisible:
			if wasShown || !sc.mon.Current().StashShown() {
				return
			}
		case ActionFocusColumnLeft, ActionFocusColumnRight, ActionMoveColumnLeft, ActionMoveColumnRight:
			level = flashColumn
		case ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown,
			ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown:
			return
		default:
			return
		}
		after := sc.mon.Layout()
		changed := id != beforeID || !slices.EqualFunc(before, after, func(a, b Placement) bool {
			return a.ID == b.ID && a.Rect == b.Rect && a.Focused == b.Focused && a.Hidden == b.Hidden && a.Floating == b.Floating
		})
		if changed && id != 0 {
			c.requestFlash(level, id)
		}
	}()
	return c.dispatchAction(a)
}

// switchesWorkspace reports a bind that may show another workspace: the
// previous, next, numbered and named focus binds, and the moves that follow
// their window (Monitor.followMove).
func switchesWorkspace(a Action) bool {
	if a == ActionFocusWorkspacePrev || a == ActionFocusWorkspaceNext {
		return true
	}
	if _, _, ok := WorkspaceArg(a); ok {
		return true
	}
	_, named := NamedArg(a)
	return named || movesToWorkspace(a)
}

func (c *Core) dispatchAction(a Action) Effect {
	if e, ok := c.cur().mon.overviewAction(a); ok {
		return e
	}
	// A bind acts on the window on screen: under a covering fullscreen
	// window, that is the one Focused reports.
	c.cur().mon.Current().FocusCover()
	// Before the edge moves below: a free float moves on its screen.
	if c.cur().mon.Current().freeAction(a) {
		return Effect{}
	}
	if name, ok := NamedArg(a); ok && c.bringNamed(name) {
		return Effect{}
	}
	switch a {
	case ActionHint:
		c.hintRequest()
		return Effect{}
	case ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown:
		if i := c.neighbor(monitorDirection(a)); i >= 0 {
			c.focusScreen = i
			c.cur().mon.focusEntry(monitorDirection(a))
		}
		return Effect{}
	case ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown:
		c.moveWorkspace(monitorDirection(a))
		return Effect{}
	case ActionFocusColumnLeft, ActionFocusColumnRight:
		w := c.cur().mon.Current()
		dir, d := 1, dirRight
		if a == ActionFocusColumnLeft {
			dir, d = -1, dirLeft
		}
		// A fullscreen float has no column in its band. Horizontal moves
		// use the existing workspace/monitor edge order; only up/down may
		// reveal the tiles below.
		edge := w.ColumnEdge(dir)
		if edge == EdgeAlways {
			c.edgeFocus(layout.Horizontal, dir, d)
			return Effect{}
		}
		// A covering fullscreen window is left for the window on that side;
		// with none (the workspace, band or stash edge), the move goes past
		// the edge (see edgeFocus) and fullscreen stays.
		if edge == EdgeIfStuck {
			if !w.FocusColumn(dir) {
				c.edgeFocus(layout.Horizontal, dir, d)
			}
			return Effect{}
		}
		// The first move from a float stays here unless nothing is under it:
		// a native float leaves for the stash or the columns, and the stash
		// keeps the focus at its ends.
		if edge == EdgeFirst && c.edgeFocus(layout.Horizontal, dir, d) {
			return Effect{}
		}
	case ActionFocusWindowUp, ActionFocusWindowDown:
		m := c.cur().mon
		// Cascade's up/down navigation stays within its bands. Let Monitor
		// handle it unchanged; only vertical workspace axes cross screens.
		if layout.WorkspaceStep(m.Current().Engine(), layout.Vertical, 1) != 0 {
			dir, d := 1, dirDown
			if a == ActionFocusWindowUp {
				dir, d = -1, dirUp
			}
			if !m.Current().FocusWindow(dir) {
				c.edgeFocus(layout.Vertical, dir, d)
			}
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
		if !Expel(from.mon.Current(), c.screens[i].mon.Current(), dir) {
			return Effect{}
		}
		c.focusScreen = i
		from.mon.normalize()
		c.cur().mon.normalize()
		return Effect{}
	case ActionMoveColumnLeft, ActionMoveColumnRight:
		// Past the edge of the workspace or cascade band the column moves to
		// the neighbor screen, on the side facing this one, and focus
		// follows it. With no screen there, a cascade column stays in its
		// band.
		w := c.cur().mon.Current()
		dir, d := 1, dirRight
		if a == ActionMoveColumnLeft {
			dir, d = -1, dirLeft
		}
		// A fullscreen window (tiled or floating) leaves for the neighbor
		// screen at once and stays fullscreen there; with no screen,
		// nothing moves.
		i := c.neighbor(d)
		if w.MovableFullscreen() {
			if i < 0 {
				return Effect{}
			}
			from := c.cur()
			to := c.screens[i].mon.Current()
			at := 0
			if d == dirLeft {
				at = len(to.Columns)
			}
			MoveFullscreen(w, to, at)
			c.focusScreen = i
			from.mon.normalize()
			c.cur().mon.normalize()
			return Effect{}
		}
		if i >= 0 && w.LeavesLane(w.Focus+dir) {
			from := c.cur()
			to := c.screens[i].mon.Current()
			at := 0
			if d == dirLeft {
				at = len(to.Columns)
			}
			if !MoveFocused(w, to, true, at) {
				return Effect{}
			}
			c.focusScreen = i
			from.mon.normalize()
			c.cur().mon.normalize()
			return Effect{}
		}
	}
	return c.cur().mon.Apply(a)
}

// edgeFocus moves focus past a content edge along axis (dir -1/1, toward d).
// Horizontal: the neighbor monitor, else an occupied workspace, else the
// trailing empty workspace. Vertical: an occupied workspace, else the
// neighbor monitor, else the trailing empty workspace. Workspace steps only
// happen on the layout's workspace axis. It reports whether focus moved.
// Callers keep local content navigation and decide which layout axes may
// cross monitors.
func (c *Core) edgeFocus(axis layout.Axis, dir int, d direction) bool {
	m := c.cur().mon
	i := c.neighbor(d)
	if monitorFirst := axis == layout.Horizontal && i >= 0; !monitorFirst {
		if to := m.occupiedStep(axis, dir); to >= 0 {
			m.Focus(to)
			return true
		}
	}
	if i >= 0 {
		c.focusScreen = i
		c.cur().mon.focusEntry(d)
		return true
	}
	return m.stepWorkspace(axis, dir)
}

// Apply runs a bind action on this monitor only. Vertical focus edges step
// workspaces; Core dispatch adds neighboring-screen navigation for those edges.
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
		// move to the next workspace if the layout's workspace axis is
		// vertical. Cascade workspaces sit side by side: up/down stop at the
		// first and last band.
		dir := 1
		if a == ActionFocusWindowUp {
			dir = -1
		}
		if !m.Current().FocusWindow(dir) {
			m.stepWorkspace(layout.Vertical, dir)
		}
		return Effect{}
	case ActionToggleFullscreen:
		m.ToggleFullscreen()
		return Effect{}
	case ActionToggleWindowStash:
		m.Current().ToggleWindowStash()
		return Effect{}
	case ActionToggleStashVisible:
		w := m.Current()
		// An empty stash has nothing to show: open the terminal in it.
		if len(w.Stash) == 0 && m.stashTerminal {
			return Effect{Spawn: true, Stash: true}
		}
		w.ToggleStashVisible()
		return Effect{}
	case ActionToggleOverview:
		m.ToggleOverview()
		return Effect{}
	case ActionMaximizeColumn:
		// A client that made itself fullscreen (Wine at monitor size) goes
		// back to its column width; the next press maximizes the column.
		if w := m.Current(); w.Fullscreen != 0 {
			if id, ok := w.Focused(); ok && id == w.Fullscreen {
				m.ToggleFullscreen()
				return Effect{}
			}
		}
	case ActionFocusWorkspacePrev, ActionFocusWorkspaceNext:
		if m.shown == nil && a == ActionFocusWorkspacePrev {
			m.Focus(m.Active - 1)
		} else if m.shown == nil {
			m.Focus(m.Active + 1)
		}
		return Effect{}
	case ActionMoveWorkspacePrev:
		m.MoveWorkspace(-1)
		return Effect{}
	case ActionMoveWorkspaceNext:
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
	case ActionMoveColumnToWorkspacePrev, ActionMoveColumnToWorkspaceNext,
		ActionMoveWindowToWorkspacePrev, ActionMoveWindowToWorkspaceNext:
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
	case ActionMoveColumnToWorkspacePrev, ActionMoveWindowToWorkspacePrev:
		return m.Active - 1, a == ActionMoveColumnToWorkspacePrev, m.Active > 0
	case ActionMoveColumnToWorkspaceNext, ActionMoveWindowToWorkspaceNext:
		return min(m.Active+1, len(m.Workspaces)-1), a == ActionMoveColumnToWorkspaceNext, true
	}
	return 0, false, false
}

// Apply runs a bind action that only touches this workspace.
func (w *Workspace) Apply(a Action) Effect {
	if commands, ok := SpawnCommands(a); ok {
		return Effect{Spawn: true, Commands: commands}
	}
	if commands, ok := StashCommands(a); ok {
		return Effect{Spawn: true, Stash: true, Commands: commands}
	}
	if w.freeAction(a) {
		return Effect{}
	}
	if axis, step, ok := ResizeArg(a); ok {
		if axis == ResizeWidth {
			for range max(step, -step) {
				if step > 0 {
					w.GrowColumn(false)
				} else {
					w.ShrinkColumn()
				}
			}
		} else {
			w.ResizeRow(step)
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
		w.GrowColumn(true)
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
