package ports

// WorkspaceActivate requests showing workspaces by stable ID, in commit order.
// Core applies the entire batch before publishing a new state.
type WorkspaceActivate struct{ IDs []uint64 }

func (WorkspaceActivate) clientEvent() {}

// Workspaces is an immutable, latest-only inventory of output workspaces.
type Workspaces struct{ Outputs []WorkspaceOutput }
type WorkspaceOutput struct {
	Name       string
	Workspaces []WorkspaceInfo
}
type WorkspaceInfo struct {
	ID             uint64
	Configured     string // configured name; empty for dynamic workspaces. ID, not this, identifies the workspace
	Name           string
	Index          int // zero-based vertical coordinate
	Active, Hidden bool
	// Frame is the workspace viewport in output-local logical pixels
	// (Monitor.Frame geometry: the whole output unless the workspace has a
	// size override). It is where a capture session of it lies.
	Frame Rect
}
