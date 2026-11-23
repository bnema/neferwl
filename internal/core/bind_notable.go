package core

// Synthetic trace names for focus moves that no bind names directly; they are
// not bindable actions.
const (
	traceEdgeFocus      = "edge-focus"
	traceSwipeWorkspace = "swipe-workspace"
)

// changesLayout reports an action that changes windows or layout, not only
// focus or view. Commands (spawn, stash commands) are left out on purpose:
// their config command lines must stay out of info logs.
func changesLayout(a Action) bool {
	switch a {
	case ActionMoveColumnLeft, ActionMoveColumnRight, ActionMoveWindowUp, ActionMoveWindowDown,
		ActionMoveColumnToWorkspacePrev, ActionMoveColumnToWorkspaceNext,
		ActionMoveWindowToWorkspacePrev, ActionMoveWindowToWorkspaceNext,
		ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown,
		ActionMoveWorkspacePrev, ActionMoveWorkspaceNext,
		ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight,
		ActionTabWindowLeft, ActionTabWindowRight, ActionToggleColumnTabbed,
		ActionMoveTabNext, ActionMoveTabPrev,
		ActionCycleColumnWidth, ActionMaximizeColumn, ActionToggleFullscreen, ActionToggleFloating,
		ActionToggleWindowStash, ActionCloseWindow, ActionQuit, ActionScaleUp, ActionScaleDown:
		return true
	}
	if _, _, ok := ResizeArg(a); ok {
		return true
	}
	if _, op, ok := WorkspaceArg(a); ok {
		return op != FocusWorkspace
	}
	op, _, _, ok := MarkArg(a)
	return ok && (op == MarkSwap || op == MarkMoveTo)
}
