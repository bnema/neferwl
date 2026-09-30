package ports

// SessionLockChanged carries display-owned protection changes to core. The
// defensive gate is engaged before this event is queued. Unlock also changes
// the generation. A dead owner leaves Protected true and no surfaces.
type SessionLockChanged struct {
	State    SecurityState
	Surfaces []LockSurfacePlacement
}

// LockSurfacePlacement is a validated mapped lock role, never a desktop window.
type LockSurfacePlacement struct {
	ID            WindowID
	Output        string
	Width, Height int
}

func (SessionLockChanged) clientEvent() {}

// SecurityInput snapshots the gate when an input event is produced, before it
// enters a forwarding queue. Stale events cannot become password/desktop input.
type SecurityInput struct {
	State SecurityState
	Event InputEvent
}

func (SecurityInput) inputEvent() {}

// SecurityCommand preserves the epoch of core input/focus commands through the
// display queue. Unwrapped commands are for standalone unlocked protocol tests.
type SecurityCommand struct {
	State   SecurityState
	Command ClientCommand
}

func (SecurityCommand) clientCommand() {}
