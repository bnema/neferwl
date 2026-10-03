package drm

import "time"

// flipStats are the flip timing counters of one output, reported and
// reset by the periodic stats entry. They are plain fields owned by the
// output goroutine. All times come from the kernel's CLOCK_MONOTONIC
// flip timestamps, so they tell a late composition from a late read of
// the event (see accountFlip).
type flipStats struct {
	// missedVblanks counts frame flips whose kernel timestamp is over 1.5
	// refresh periods after the previous frame flip (never under VRR,
	// where the period is not fixed).
	missedVblanks int
	// maxInterval is the longest time between two frame flips.
	maxInterval time.Duration
	// maxFlipToRead is the longest time between a flip's kernel timestamp
	// and the output reading its event: the event delivery latency.
	maxFlipToRead time.Duration
	// lateFences counts frame flips whose fences signalled after the
	// vblank before the flip (never under VRR). Fence times are only read
	// while flip tracing is on.
	lateFences int
}

// refreshPeriod is the time between two vblanks of the current mode (0:
// not fixed, VRR).
func (o *Output) refreshPeriod() time.Duration {
	if o.vrrOn {
		return 0
	}
	return time.Duration(int64(time.Second) * 1000 / int64(max(1, o.mode.refreshMilli())))
}

// accountFlip counts the completion ev of a commit, handled at now (both
// CLOCK_MONOTONIC). fenceAt is when the commit's fences signalled (0:
// none or unknown). It updates lastFlipAt for frame flips and never
// allocates.
//
//   - A flip that follows the previous one by over 1.5 periods missed a
//     vblank: either the frame was not ready (late fence) or the commit
//     came late.
//   - A late fence signalled after ev.when-period: the frame could not
//     have flipped on the vblank before.
//   - A flip on time whose event is read late is a delivery delay of the
//     event reader, not a composition delay.
func (o *Output) accountFlip(ev flipEvent, frame bool, now, fenceAt time.Duration) {
	s := &o.flipStats
	s.maxFlipToRead = max(s.maxFlipToRead, now-ev.when)
	if !frame {
		return
	}
	period := o.refreshPeriod()
	if o.lastFlipAt != 0 {
		interval := ev.when - o.lastFlipAt
		s.maxInterval = max(s.maxInterval, interval)
		if period != 0 && 2*interval > 3*period {
			s.missedVblanks++
		}
	}
	o.lastFlipAt = ev.when
	if period != 0 && fenceAt != 0 && fenceAt > ev.when-period {
		s.lateFences++
	}
}

// takeFlipStats returns the counters and starts over.
func (o *Output) takeFlipStats() flipStats {
	s := o.flipStats
	o.flipStats = flipStats{}
	return s
}
