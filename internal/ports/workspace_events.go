package ports

// WorkspaceActivate requests showing workspaces by stable ID, in commit order.
// Core applies the entire batch before publishing a new state.
type WorkspaceActivate struct{ IDs []uint64 }

func (WorkspaceActivate) clientEvent() {}
