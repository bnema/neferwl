package domain

// ProcKey identifies a process in the compositor's PID namespace. Start is
// Linux starttime in clock ticks, distinguishing reuse of a PID.
type ProcKey struct {
	PID   int
	Start uint64
}

// ProcNode is a best-effort placement hint, never an authorization credential.
type ProcNode struct {
	// Comm is the bounded Linux process name, at most 16 bytes.
	Comm          string
	Key           ProcKey
	SessionLeader bool
}
