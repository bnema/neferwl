package motion

import "time"

// Slide is a view offset that a swipe sets while the fingers follow it, and
// a spring then takes to 0 (off: pixels for the columns, windows for the
// stash, workspaces for the monitor). Pure presentation over settled state.
type Slide struct {
	// Off is the presentation offset from the settled state, in the caller's units.
	Off float64
	// Motion carries Off back to zero after the fingers lift.
	Motion Motion
}

// Stop lands the slide where its state says.
func (s *Slide) Stop() { *s = Slide{} }

// Step moves a running spring to now and stops it once it settles.
func (s *Slide) Step(now time.Time) {
	if !s.Motion.On {
		return
	}
	v, done := s.Motion.At(now)
	s.Off = v
	if done {
		s.Stop()
	}
}

// Busy reports whether the slide is off its state or its spring runs.
func (s *Slide) Busy() bool { return s.Motion.On || s.Off != 0 }
