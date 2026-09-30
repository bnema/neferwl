package drm

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// frameLifecycle is owned by Output.Run. KMS and the event reader do not
// schedule frames: Run feeds this state with commit results, flip serials and
// deadline ticks. Only Run uses the timer; the transitions take plain values.
type frameLifecycle struct {
	pending                   bool
	pendingFrame              pendingFrame
	serials                   *atomic.Uint64 // shared across outputs on the card
	pendingSerial, nextSerial uint64
	flipStart                 time.Time
	stuckAfter                time.Duration
	stuckAt                   time.Time
	fenceWarned               bool
	busySince, lastBusy       time.Time
	stuckTimer                *time.Timer
}

// pendingFrame is what the pending frame commit shows.
type pendingFrame struct {
	security ports.SecurityState // admission epoch checked again at KMS boundary
	frame    bool
	queued   uint64
	zeroCopy ports.WindowID // window shown without composition
	async    bool
	shows    map[ports.WindowID]uint64
	// fences are duplicates of the fences the commit waits on, owned until
	// the commit ends. composed is set when the frame was rendered.
	fences   []*os.File
	composed bool
	// fenceReady: every fence had signalled at commit (flip tracing only).
	fenceReady bool
}

func (f *pendingFrame) closeFences() {
	for _, fd := range f.fences {
		fd.Close()
	}
	f.fences = nil
}

// Commit event userData: the commit serial above userKindBits, the kind below.
const (
	userFrame    = 1
	userState    = 2
	userKindBits = 2
)

const (
	stuckTimeout = time.Second
	busyRetry    = 50 * time.Millisecond
)

// stuckLimit returns the configured recovery deadline or its default.
func (l *frameLifecycle) stuckLimit() time.Duration {
	if l.stuckAfter > 0 {
		return l.stuckAfter
	}
	return stuckTimeout
}

// userData numbers a commit across all outputs of the card.
func (l *frameLifecycle) userData(kind uint64) uint64 {
	l.nextSerial = l.serials.Add(1)
	return l.nextSerial<<userKindBits | kind
}

// endPending releases the in-flight commit and its duplicated fences.
func (l *frameLifecycle) endPending() {
	l.pendingFrame.closeFences()
	l.pending, l.pendingFrame = false, pendingFrame{}
}

// begin records a successful nonblocking commit awaiting its event.
func (l *frameLifecycle) begin(f pendingFrame, now time.Time) {
	l.pendingSerial = l.nextSerial
	l.pendingFrame.closeFences()
	l.pending, l.pendingFrame, l.flipStart = true, f, now
	l.stuckAt, l.fenceWarned = now.Add(l.stuckLimit()), false
	l.busySince = time.Time{}
}

// pendingCommit reports whether the output must wait before another commit.
func (l *frameLifecycle) pendingCommit() bool { return l.pending }

// resetAfterModeset ends a replaced commit and clears the old busy run.
func (l *frameLifecycle) resetAfterModeset() {
	l.endPending()
	l.busySince = time.Time{}
}

// deadlineReached reports whether a pending commit is ready for recovery.
func (l *frameLifecycle) deadlineReached(now time.Time) bool {
	return l.pending && !now.Before(l.stuckAt)
}

// deadlineInfo is the pending commit's log context and fences at a tick.
// The caller polls the fences; the transition itself consumes a plain bool.
func (l *frameLifecycle) deadlineInfo(now time.Time) (serial uint64, frame bool, age, busy time.Duration, fences []*os.File) {
	return l.pendingSerial, l.pendingFrame.frame, now.Sub(l.flipStart), now.Sub(l.busySince), l.pendingFrame.fences
}

// busy records an EBUSY refusal. No commit of ours is known to be in
// flight: any event of this CRTC may unblock the retry.
// It returns true when this is a new run, for the caller's log.
func (l *frameLifecycle) busy(now time.Time) bool {
	l.endPending()
	l.pending, l.flipStart, l.stuckAt = true, now, now.Add(busyRetry)
	l.pendingSerial = 0
	newRun := l.busySince.IsZero() || now.Sub(l.lastBusy) > 4*busyRetry
	if newRun {
		l.busySince = now
	}
	l.lastBusy = now
	return newRun
}

// ours reports whether user is the event of the pending commit with a
// known serial (not the end of an EBUSY wait).
func (l *frameLifecycle) ours(user uint64) bool {
	return l.pending && l.pendingSerial != 0 && user>>userKindBits == l.pendingSerial
}

// flip consumes only the event for the pending commit; while waiting after
// EBUSY there is no known serial, so any CRTC event ends that wait. The
// returned start lets Output measure flip age after pending fences close.
func (l *frameLifecycle) flip(user uint64) (pendingFrame, time.Time, bool) {
	if !l.pending || l.pendingSerial != 0 && user>>userKindBits != l.pendingSerial {
		return pendingFrame{}, time.Time{}, false
	}
	f, start := l.pendingFrame, l.flipStart
	l.endPending()
	l.busySince = time.Time{}
	return f, start, true
}

// timeoutResult describes the action after a deadline tick.
type timeoutResult uint8

const (
	timeoutEarly   timeoutResult = iota // deadline not reached (or a warned fence still waits)
	timeoutFence                        // first wait for an unsignalled fence
	timeoutRetry                        // EBUSY without an event: retry the commit
	timeoutBusy                         // EBUSY past its limit: modeset
	timeoutMissing                      // committed event missing: modeset
)

// timeout takes the current fence status as a value. The caller polls only
// when a known commit reaches its deadline; no clock, fence or event source
// is mocked in transition tests.
func (l *frameLifecycle) timeout(now time.Time, fencesReady bool) timeoutResult {
	if !l.pending || now.Before(l.stuckAt) {
		return timeoutEarly
	}
	if l.pendingSerial != 0 && !fencesReady {
		l.stuckAt = now.Add(l.stuckLimit())
		if !l.fenceWarned {
			l.fenceWarned = true
			return timeoutFence
		}
		return timeoutEarly
	}
	l.endPending()
	if l.pendingSerial != 0 {
		return timeoutMissing
	}
	if l.busySince.IsZero() || now.Sub(l.busySince) < l.stuckLimit() {
		return timeoutRetry
	}
	return timeoutBusy
}

// startTimer creates the per-output deadline timer for Run, not kms.
func (l *frameLifecycle) startTimer() {
	l.stuckTimer = time.NewTimer(time.Hour)
	l.stuckTimer.Stop()
}

// wait arms the pending deadline, or disables it while idle or away.
// Go 1.27 guarantees a stopped/reset timer cannot deliver an old value.
func (l *frameLifecycle) wait(enabled bool) <-chan time.Time {
	if !enabled || !l.pending {
		l.stuckTimer.Stop()
		return nil
	}
	l.stuckTimer.Reset(max(0, time.Until(l.stuckAt)))
	return l.stuckTimer.C
}

// stopTimer releases the timer when Run exits.
func (l *frameLifecycle) stopTimer() { l.stuckTimer.Stop() }
