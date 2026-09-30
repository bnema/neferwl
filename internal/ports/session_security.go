package ports

// LockGeneration identifies a session transition. Zero is the initial unlocked epoch.
type LockGeneration uint64

// SecurityState is one coherent admission epoch. Generation changes on both
// acquisition and release so queued work cannot cross a lock transition.
type SecurityState struct {
	Generation LockGeneration
	Protected  bool
}

// SessionSecurity exposes the defensive gate to off-display boundaries.
// The display goroutine remains the sole authority for protocol admission.
type SessionSecurity interface {
	Snapshot() SecurityState
}

// SessionSecurityController is owned by the display goroutine. Releasing
// protection requires its exact acquisition generation; crashes do not release.
type SessionSecurityController interface {
	SessionSecurity
	Engage() (SecurityState, error)
	Release(expected LockGeneration) (SecurityState, error)
}
