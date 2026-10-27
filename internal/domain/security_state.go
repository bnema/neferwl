package domain

// SecurityState is one coherent admission epoch. Generation changes on both
// acquisition and release so queued work cannot cross a lock transition.
type SecurityState struct {
	Generation LockGeneration
	Protected  bool
}
