package drm

import "time"

// masterRetry retries an enable modeset that the kernel refused because
// DRM master was not back yet. The seat can report the session active
// before the kernel hands master back; without a retry the output stays
// dark until the next VT switch.
type masterRetry struct {
	pending  bool
	attempts int
}

const (
	masterRetryFirst = 20 * time.Millisecond
	masterRetryMax   = time.Second
	// masterRetryLimit bounds the retries of one enable (about 30 s).
	masterRetryLimit = 40
)

// refused records an enable refused with err; only a lost master is
// retried, and enabled stays false until a retry succeeds.
func (m *masterRetry) refused(err error, enabled *bool) {
	if !lostMaster(err) || *enabled {
		return
	}
	m.pending = true
}

// done ends the retry: the enable succeeded or the seat changed again.
func (m *masterRetry) done() { *m = masterRetry{} }

// next is the delay before the next attempt, if one is due.
func (m *masterRetry) next() (time.Duration, bool) {
	if !m.pending || m.attempts >= masterRetryLimit {
		return 0, false
	}
	return min(masterRetryFirst<<min(m.attempts, 6), masterRetryMax), true
}
