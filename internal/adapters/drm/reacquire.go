package drm

import "time"

// masterRetry retries an enable modeset that the kernel refused because
// DRM master was not back yet. The seat can report the session active
// before the kernel hands master back; without a retry the output stays
// dark until the next VT switch.
//
// The retry has a fixed deadline: the output loop wakes for many other
// events (pings, cursor, reports) and must not push it back on each one.
type masterRetry struct {
	pending  bool
	due      time.Time
	attempts int
}

const (
	masterRetryFirst = 20 * time.Millisecond
	masterRetryMax   = time.Second
	// masterRetryLimit bounds the retries of one enable (about 35 s).
	masterRetryLimit = 40
)

// refused records an enable refused with err at now; only a lost master
// is retried, and enabled stays false until a retry succeeds. It reports
// that the retries are exhausted.
func (m *masterRetry) refused(err error, enabled bool, now time.Time) (gaveUp bool) {
	if !lostMaster(err) || enabled {
		return false
	}
	if m.attempts >= masterRetryLimit {
		m.pending = false
		return true
	}
	m.pending = true
	m.due = now.Add(min(masterRetryFirst<<min(m.attempts, 6), masterRetryMax))
	return false
}

// fire starts an attempt: the retry is pending again only if it is refused.
func (m *masterRetry) fire() {
	m.pending = false
	m.attempts++
}

// done ends the retry: the enable succeeded or the seat changed.
func (m *masterRetry) done() { *m = masterRetry{} }

// next is the time left before the next attempt, if one is due.
func (m *masterRetry) next(now time.Time) (time.Duration, bool) {
	if !m.pending {
		return 0, false
	}
	return max(0, m.due.Sub(now)), true
}
