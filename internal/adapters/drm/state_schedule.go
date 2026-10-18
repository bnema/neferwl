package drm

import "time"

// State commit scheduling: a CRTC takes one atomic commit per vblank, so a
// commit carrying only state (cursor move, VRR toggle, content type) holds
// the next frame until it lands. Committed as soon as the pointer moves, it
// pushes a video frame that arrives later in the same refresh period to the
// following vblank: a visible stutter on every cursor move over the output
// or across to another one.
//
// So a state change waits until stateLead before the next vblank. A frame
// committed before then carries it (one commit, no lost vblank); otherwise
// the state commits alone and still lands on that vblank, so the cursor is
// never later than it would have been.
//
// Under VRR there is no fixed vblank to aim at: state commits at once (a
// game's cursor rides on its frames, see cursorWaits).

// stateLeadMax is how long before the vblank a deferred state commit is
// made: enough for the kernel to program the planes for that vblank.
const stateLeadMax = 1500 * time.Microsecond

// vblankStale is how old the last vblank may be to predict the next one: a
// longer idle period lets the nominal period drift from the panel's.
const vblankStale = 250 * time.Millisecond

// stateLead is stateLeadMax, at most a quarter of period.
func stateLead(period time.Duration) time.Duration { return min(stateLeadMax, period/4) }

// stateDelay is how long a state change made at now (CLOCK_MONOTONIC)
// waits before its own commit: until stateLead before the next vblank, 0
// when that point has passed or no vblank can be predicted (VRR, no recent
// flip).
func (o *Output) stateDelay(now time.Duration) time.Duration {
	period := o.refreshPeriod()
	last := o.lastVblankAt
	if period <= 0 || last <= 0 || now < last || now-last > vblankStale {
		return 0
	}
	next := last + ((now-last)/period+1)*period
	return max(0, next-stateLead(period)-now)
}

// stateChanged reports whether a state commit would change anything:
// cursor, VRR or content type differ from what KMS holds.
func (o *Output) stateChanged(vrr bool) bool {
	if o.cursor != nil && o.cursor.desired() != o.cursor.applied {
		return true
	}
	if vrr != o.vrrOn {
		return true
	}
	return o.contentProp != 0 && o.contentWanted != o.contentValue
}

// stateDue reports whether the state commit may go now. The first call
// for a change fixes its time (stateAt) and later calls keep it, so a
// timer that fires late never pushes the commit by another period.
func (o *Output) stateDue(now time.Time, mono time.Duration) bool {
	if o.stateAt.IsZero() {
		o.stateAt = now.Add(o.stateDelay(mono))
	}
	if now.Before(o.stateAt) {
		return false
	}
	o.stateAt = time.Time{}
	return true
}

// noteVblank records the vblank of a completed vsynced commit (frame or
// state). A tearing flip's timestamp is not a vblank.
func (o *Output) noteVblank(f pendingFrame, when time.Duration) {
	if f.async || when <= 0 {
		return
	}
	o.lastVblankAt = when
}
