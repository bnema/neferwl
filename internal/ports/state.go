package ports

// State carries core → state publisher a snapshot of what is on screen, for
// scripts (bars, status lines). Workspace numbers count from 1.
type State struct {
	// Output is the focused output; "" before the first one.
	Output  string
	Outputs []OutputState
	// Window is the focused window, nil when none.
	Window  *WindowState
	Windows []WindowState
}

// OutputState is one output: its workspace on screen and how many numbered
// workspaces it has. Active is 0 while a hidden (named) workspace is shown.
type OutputState struct {
	Name      string
	Active    int
	Count     int
	Workspace string // configured name of the workspace on screen, "" if unnamed
	// WorkspaceID is the core ID of the workspace on screen (WorkspaceInfo.ID).
	WorkspaceID uint64
}

// WindowState is one mapped window and where it is.
type WindowState struct {
	ID     WindowID
	AppID  string
	PID    int
	Output string
	// Workspace is the window's numbered workspace, 0 for a hidden one.
	Workspace int
	// WorkspaceID is the core ID of the window's workspace, numbered or not
	// (WorkspaceInfo.ID); WorkspaceName is its configured name, "" for a
	// dynamic one (WorkspaceInfo.Configured).
	WorkspaceID   uint64
	WorkspaceName string
	// Visible means on the workspace on screen (it may be behind a
	// fullscreen window).
	Visible bool
	// IdleInhibit is set while the window keeps the session from idling.
	IdleInhibit bool `json:",omitempty"`
	// Floating is set for native floats and stashed windows.
	Floating bool
	// StashIndex is the 1-based place of a stashed window in its stash of
	// StashCount windows; both are 0 outside the stash.
	StashIndex, StashCount int
	// Column and Row are the 1-based place of a tiled window: its column in
	// layout order (not geometry under fixed overflow), its row top to
	// bottom in that column; both are 0 for floating windows.
	Column, Row int
	// Hidden is set for a stashed window while its stash is hidden.
	Hidden bool
}
