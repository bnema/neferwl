package domain

// State carries core → state publisher a snapshot of what is on screen, for
// scripts (bars, status lines). Workspace numbers count from 1.
type State struct {
	// Output is the focused output; "" before the first one.
	Output  string
	Outputs []OutputState
	// Window is the focused window, nil when none.
	Window  *WindowState
	Windows []WindowState
	// Workspaces is every workspace of every output.
	Workspaces []WorkspaceState
}

// WorkspaceState is one workspace and its tiled columns, left to right in
// layout order. Rects are logical pixels in the output's coordinates.
type WorkspaceState struct {
	ID       uint64
	Number   int    // numbered workspace, 0 for a hidden/named one
	Name     string // configured name, "" if dynamic
	Output   string
	Visible  bool   // on screen on its output
	Overflow string // "fixed", "scroll", "cascade"
	Columns  []ColumnState
}

// ColumnState is one column and its tiled windows.
type ColumnState struct {
	Rect    Rect        // column area, view offset applied (may be off-screen)
	Windows []TileState // top to bottom
}

// TileState is one tiled window and its settled rectangle.
type TileState struct {
	ID   WindowID
	Rect Rect
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
	// Sandbox is the client's security context app id, else its engine,
	// "" when not sandboxed.
	Sandbox string `json:",omitempty"`
	// ContentType is "photo", "video", "game", or "" without a hint.
	ContentType string `json:",omitempty"`
	// Xwayland is set for an X11 window served by the satellite session.
	Xwayland bool `json:",omitempty"`
	// IdleInhibit is set while the window keeps the session from idling.
	IdleInhibit bool `json:",omitempty"`
	// ShortcutsInhibited is set while the window asks to receive compositor shortcuts (keyboard_shortcuts_inhibit); core grants it only while it has keyboard focus.
	ShortcutsInhibited bool `json:",omitempty"`
	// PointerConstraint is "locked" or "confined" while the window holds the pointer constraint, "" otherwise.
	PointerConstraint string `json:",omitempty"`
	// Floating is set for native floats and stashed windows.
	Floating bool
	// Fullscreen is set for its workspace's fullscreen window, also while a
	// stashed window shows over it or its workspace is not shown.
	Fullscreen bool
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
