package drm

import "time"

// VRR flip gap: a workaround for game frames that hit the panel's slowest
// refresh while the game renders fast.
//
// Symptom: fullscreen game, direct scanout, VRR on, 120-165 fps. Now and
// then, runs of 5 to 30 consecutive flips each take ~20.8 ms: the panel's
// maximum VRR frame time (1/48 Hz on a 48-165 Hz panel). The game's FPS
// counter stays steady, but the screen visibly drops to 48 Hz for a
// fraction of a second.
//
// Root cause, measured with --debug=drm-flip on amdgpu (DP, 48-165 Hz):
//   - the client's acquire fence had already signalled at commit, and
//     the atomic ioctl returned at once: neither the game nor our commit
//     path is late;
//   - every slow flip was committed within ~0.3 ms after the previous
//     flip event, i.e. right at the start of the next refresh cycle;
//   - the flip event then comes ~20.8 ms after the commit: the flip
//     misses the early-refresh window and the display only refreshes when
//     the VRR timeout forces it (vtotal at the minimum rate);
//   - since Output.Run commits the next frame as soon as that late event
//     arrives, the next commit lands at the same point of the cycle, and
//     the output stays locked at the minimum rate until the game's timing
//     drifts.
//
// Why the kernel misses these flips is not established (amdgpu DC VRR
// flip programming around vblank is the likely place). What is measured:
// committing the next game frame a short time after the flip event keeps
// it out of that window. With a 1 ms gap a 3-minute run had 0 slow flips
// in gameplay, against bursts of up to 30 without it and ~10 isolated
// slow flips per minute with a 2 ms delay applied only after a slow flip.
// The cost is at most the gap of added latency per frame, well under one
// 165 Hz refresh (6 ms), and it applies only while VRR runs for a game.
//
// render.vrr-flip-gap sets the gap (default 1 ms, 0 turns the workaround
// off); drop it once the driver no longer needs it.

// startFlipGap is called on each completed commit f: after a game frame's
// flip under VRR, the next frame waits vrrFlipGap before it commits. A
// state commit (cursor, VRR toggle) completing inside the gap keeps it:
// the next frame still follows the last frame flip.
func (o *Output) startFlipGap(f pendingFrame) {
	if !f.frame {
		return
	}
	o.flipGapUntil, o.flipGapAt = time.Time{}, 0
	if o.vrrFlipGap > 0 && o.vrrOn && o.vrrGame {
		o.flipGapUntil = time.Now().Add(o.vrrFlipGap)
		o.flipGapAt = monotonic() + o.vrrFlipGap
	}
}
