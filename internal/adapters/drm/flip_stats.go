package drm

import "time"

// flipStats are the flip timing counters of one output, reported and
// reset by the periodic stats entry. They are plain fields owned by the
// output goroutine. All times come from the kernel's CLOCK_MONOTONIC
// flip timestamps, so they tell a late composition from a late read of
// the event (see accountFlip).
type flipStats struct {
	// missedVblanks counts due frame flips whose kernel timestamp is over
	// 1.5 refresh periods after the previous frame flip (never under VRR,
	// where the period is not fixed).
	missedVblanks int
	// maxInterval is the longest time between two frame flips, of the
	// frames that were due.
	maxInterval time.Duration
	// maxCommitDelay is the longest time a due frame waited between the
	// moment it was due (wanted, and the previous flip landed) and its
	// commit: late event read, slow composition.
	maxCommitDelay time.Duration
	// maxFlipToRead is the longest time between a flip's kernel timestamp
	// and the output reading its event: the event delivery latency.
	maxFlipToRead time.Duration
	// lateFences counts missed vblanks whose fences signalled after the
	// vblank the frame targeted (never under VRR). Fence times are only
	// read while flip tracing is on.
	lateFences int
}

// nominalPeriod is the time between two vblanks at the mode's refresh
// rate. Under VRR it is the shortest period: the fastest the output
// flips.
func (o *Output) nominalPeriod() time.Duration {
	return time.Duration(int64(time.Second) * 1000 / int64(max(1, o.mode.refreshMilli())))
}

// refreshPeriod is the time between two vblanks of the current mode (0:
// not fixed, VRR).
func (o *Output) refreshPeriod() time.Duration {
	if o.vrrOn {
		return 0
	}
	return o.nominalPeriod()
}

// setVRR records the VRR state. The flip interval chain restarts when it
// changes: the period of the two states differs.
func (o *Output) setVRR(on bool) {
	if o.vrrOn != on {
		o.lastFlipAt = 0
	}
	o.vrrOn = on
}

// accountFlip counts the completion ev of a frame commit made at
// commitAt for a frame wanted at wantedAt (0: unknown, the commit time)
// (all times CLOCK_MONOTONIC). fenceAt is when the commit's fences
// signalled (0: none or unknown). It updates lastFlipAt for frame flips
// and never allocates.
//
// An output renders only on change, so a gap after an idle period says
// nothing about missed vblanks. The interval to the previous flip counts
// only for a frame that was due: wanted less than one period after the
// previous flip, so it was meant for the very next vblank, whenever it was
// committed. A frame that waited for the previous flip's event or for its
// composition is due as soon as it is wanted. Then:
//
//   - a flip over 1.5 periods after the previous one missed a vblank
//     (never counted under VRR, where the period is not fixed);
//   - if it missed one and its fences signalled after the vblank it
//     targeted (the previous flip plus one period), the frame was late: a
//     late fence. A fence before that vblank is not;
//   - the commit delay is the time from when the frame was due (wanted,
//     and not before the previous flip) to its commit. A missed vblank
//     with a long commit delay and no late fence points at the commit
//     path: late event read, slow composition on the CPU.
func (o *Output) accountFlip(ev flipEvent, frame bool, wantedAt, commitAt, fenceAt time.Duration) {
	if !frame {
		return
	}
	if wantedAt == 0 {
		wantedAt = commitAt
	}
	if prev := o.lastFlipAt; prev != 0 && wantedAt-prev < o.nominalPeriod() {
		s := &o.flipStats
		interval := ev.when - prev
		s.maxInterval = max(s.maxInterval, interval)
		s.maxCommitDelay = max(s.maxCommitDelay, commitAt-max(wantedAt, prev))
		if period := o.refreshPeriod(); period != 0 && 2*interval > 3*period {
			s.missedVblanks++
			if fenceAt != 0 && fenceAt > prev+period {
				s.lateFences++
			}
		}
	}
	o.lastFlipAt = ev.when
}

// accountRead tracks the delivery latency of the event ev handled at now.
func (o *Output) accountRead(ev flipEvent, now time.Duration) {
	o.flipStats.maxFlipToRead = max(o.flipStats.maxFlipToRead, now-ev.when)
}

// takeFlipStats returns the counters and starts over.
func (o *Output) takeFlipStats() flipStats {
	s := o.flipStats
	o.flipStats = flipStats{}
	return s
}
