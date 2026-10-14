package screencast

// shotState is the screenshot in flight; one at most.
type shotState struct{ busy bool }

// shotResult is what a finished nefercap run leaves for the owner.
type shotResult struct {
	ready bool
	err   error
}

func (s *Service) onShotDone(r *shotResult) { _ = r }
