package ports

import "github.com/bnema/neferwl/internal/domain"

// SessionSecurity exposes the defensive gate to off-display boundaries.
// The display goroutine remains the sole authority for protocol admission.
type SessionSecurity interface {
	Snapshot() domain.SecurityState
}

// SessionSecurityController is owned by the display goroutine. Releasing
// protection requires its exact acquisition generation; crashes do not release.
type SessionSecurityController interface {
	SessionSecurity
	Engage() (domain.SecurityState, error)
	Release(expected domain.LockGeneration) (domain.SecurityState, error)
}
