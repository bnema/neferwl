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

// nominalPeriod is the time between two vblanks at the mode's refresh
// rate, which is also the longest a VRR output is held to.
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

// accountFlip counts the completion ev of the commit made at commitAt
// (all times CLOCK_MONOTONIC). fenceAt is when the commit's
// fences signalled (0: none or unknown). It updates lastFlipAt for frame
// flips and never allocates.
//
// An output only commits on change, so a gap after an idle period says
// nothing about missed vblanks. The interval to the previous flip counts
// only if the commit came less than one period after it, that is if the
// commit could have made the very next vblank. Then:
//
//   - a flip over 1.5 periods after the previous one missed a vblank
//     (never counted under VRR, where the period is not fixed);
//   - if it missed one and its fences signalled after the vblank the
//     commit targeted (the previous flip plus one period), the frame was
//     late: a late fence. A flip on time or a fence before the targeted
//     vblank is not;
//   - a flip on time whose event is read late is a delay of the event
//     reader, not of the composition: see maxFlipToRead.
func (o *Output) accountFlip(ev flipEvent, frame bool, commitAt, fenceAt time.Duration) {
	if !frame {
		return
	}
	nominal := o.nominalPeriod()
	if prev := o.lastFlipAt; prev != 0 && commitAt-prev < nominal {
		s := &o.flipStats
		interval := ev.when - prev
		s.maxInterval = max(s.maxInterval, interval)
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
