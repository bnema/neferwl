package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
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
	if c.cur().mon.Current().ApplyFree(a) {
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
			if !MoveFullscreen(w, to, at) {
				return Effect{}
			}
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
			if !MoveColumnTo(w, to, at) {
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
