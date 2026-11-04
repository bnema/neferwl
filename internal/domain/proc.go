package domain

// ProcKey identifies a process in the compositor's PID namespace. Start is
// Linux starttime in clock ticks, distinguishing reuse of a PID.
type ProcKey struct {
	PID   int
	Start uint64
}

// ProcNode is a best-effort placement hint, never an authorization credential.
type ProcNode struct {
	Key           ProcKey
	SessionLeader bool
}
