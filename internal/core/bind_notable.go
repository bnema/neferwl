package core

// focusOnly reports an action that only moves focus or changes the view,
// leaving windows and layout as they were.
func focusOnly(a Action) bool {
	switch a {
	case ActionFocusColumnLeft, ActionFocusColumnRight, ActionFocusWindowUp, ActionFocusWindowDown,
		ActionFocusWorkspacePrev, ActionFocusWorkspaceNext,
		ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown,
		ActionSwitchColumnNext, ActionSwitchColumnPrev,
		ActionToggleOverview, ActionToggleStashVisible, ActionHint, ActionOSDPin:
		return true
	}
	_, op, ok := WorkspaceArg(a)
	return ok && op == FocusWorkspace
}
