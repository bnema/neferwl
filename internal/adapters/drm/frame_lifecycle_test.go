package drm

import (
	"sync/atomic"
	"testing"
	"time"
)

// All transitions below use values only: no event reader, timer, clock or
// KMS mock is needed to prove the serial and deadline rules.
func TestFrameLifecycleStaleFlipAndRecovery(t *testing.T) {
	var serials atomic.Uint64
	l := frameLifecycle{serials: &serials}
	start := time.Unix(100, 0)
	old := l.userData(userFrame)
	l.begin(pendingFrame{frame: true, queued: 7}, start)
	l.endPending() // successful recovery modeset invalidates the old event
	current := l.userData(userFrame)
	l.begin(pendingFrame{frame: true, queued: 8}, start.Add(time.Millisecond))
	if _, _, ok := l.flip(old); ok || !l.pending {
		t.Fatal("old event completed new commit")
	}
	f, _, ok := l.flip(current)
	if !ok || l.pending || !f.frame || f.queued != 8 {
		t.Fatalf("new event: ok=%v pending=%v frame=%+v", ok, l.pending, f)
	}
}

func TestFrameLifecycleBusyRetryAndLimit(t *testing.T) {
	var serials atomic.Uint64
	l := frameLifecycle{serials: &serials, stuckAfter: 200 * time.Millisecond}
	start := time.Unix(100, 0)
	if !l.busy(start) || l.stuckAt != start.Add(busyRetry) {
		t.Fatal("initial EBUSY did not arm retry")
	}
	if got := l.timeout(start.Add(busyRetry-time.Nanosecond), true); got != timeoutEarly {
		t.Fatalf("premature deadline: %d", got)
	}
	if got := l.timeout(start.Add(busyRetry), true); got != timeoutRetry || l.pending {
		t.Fatalf("first retry: %d pending %v", got, l.pending)
	}
	l.busy(start.Add(busyRetry))
	if _, _, ok := l.flip(999 << userKindBits); !ok || l.pending {
		t.Fatal("CRTC event did not unblock EBUSY")
	}
	for i := 3; i < 6; i++ {
		l.busy(start.Add(time.Duration(i) * busyRetry))
		if got := l.timeout(start.Add(time.Duration(i+1)*busyRetry), true); got != timeoutRetry {
			t.Fatalf("busy retry %d: %d", i, got)
		}
	}
	l.busy(start.Add(6 * busyRetry))
	if got := l.timeout(start.Add(7*busyRetry), true); got != timeoutBusy {
		t.Fatalf("long EBUSY did not request modeset: %d", got)
	}
	if !l.busy(start.Add(time.Second)) {
		t.Fatal("later refusal did not start a new busy run")
	}
	if got := l.timeout(start.Add(time.Second+busyRetry), true); got != timeoutRetry {
		t.Fatalf("new busy run: %d", got)
	}
}

func TestFrameLifecycleFenceAndMissingEvent(t *testing.T) {
	var serials atomic.Uint64
	l := frameLifecycle{serials: &serials, stuckAfter: 100 * time.Millisecond}
	start := time.Unix(100, 0)
	user := l.userData(userFrame)
	l.begin(pendingFrame{frame: true}, start)
	if got := l.timeout(start.Add(100*time.Millisecond), false); got != timeoutFence || !l.pending || l.stuckAt != start.Add(200*time.Millisecond) {
		t.Fatalf("unsignalled fence: %d pending %v deadline %v", got, l.pending, l.stuckAt)
	}
	if got := l.timeout(start.Add(200*time.Millisecond), false); got != timeoutEarly || !l.pending {
		t.Fatalf("repeated fence wait: %d", got)
	}
	if got := l.timeout(start.Add(300*time.Millisecond), true); got != timeoutMissing || l.pending {
		t.Fatalf("missing event after fence: %d pending %v", got, l.pending)
	}
	if _, _, ok := l.flip(user); ok {
		t.Fatal("late event completed abandoned commit")
	}
}

func TestFrameLifecycleTransitionsAllocations(t *testing.T) {
	var serials atomic.Uint64
	l := frameLifecycle{serials: &serials}
	start := time.Unix(100, 0)
	if allocs := testing.AllocsPerRun(100, func() {
		user := l.userData(userFrame)
		l.begin(pendingFrame{frame: true}, start)
		l.flip(user)
		l.busy(start)
		l.timeout(start.Add(busyRetry), true)
	}); allocs != 0 {
		t.Fatalf("frame lifecycle: %.1f allocs, want 0", allocs)
	}
}

func TestFrameLifecycleNoMemBackoffAndRecovery(t *testing.T) {
	l := frameLifecycle{stuckAfter: time.Millisecond}
	start := time.Unix(100, 0)
	now := start
	for i := range 20 {
		attempt, fresh := l.nomem(now)
		want := min(nomemRetryMax, nomemRetryMin*time.Duration(1<<min(i, 7)))
		if attempt != i+1 || fresh != (i == 0) || l.nomemBackoff != want || l.stuckAt != now.Add(want) {
			t.Fatalf("attempt %d fresh=%v backoff=%v deadline=%v", attempt, fresh, l.nomemBackoff, l.stuckAt)
		}
		if _, _, ok := l.flip(123 << userKindBits); ok {
			t.Fatal("stale event bypassed memory backoff")
		}
		if got := l.timeout(l.stuckAt.Add(10*l.stuckLimit()), true); got != timeoutRetry {
			t.Fatalf("ENOMEM escalated: %d", got)
		}
		now = l.stuckAt
	}
	attempts, outage := l.nomemRecovered(now)
	if attempts != 20 || outage != now.Sub(start) || !l.nomemSince.IsZero() || !l.lastNomem.IsZero() || l.nomemBackoff != 0 || l.nomemAttempts != 0 {
		t.Fatalf("recovery: %d %v %+v", attempts, outage, l)
	}
	l.nomem(now)
	l.resetAfterModeset()
	if !l.nomemSince.IsZero() || l.nomemAttempts != 0 || l.pending {
		t.Fatal("modeset did not clear run")
	}
}

func TestFrameLifecycleNoMemThenBusyEvent(t *testing.T) {
	l := frameLifecycle{}
	start := time.Unix(100, 0)
	l.nomem(start)
	if got := l.timeout(l.stuckAt, true); got != timeoutRetry {
		t.Fatalf("memory wait: %v", got)
	}
	l.busy(start.Add(nomemRetryMin))
	if _, _, ok := l.flip(123 << userKindBits); !ok || l.pending || l.waitKind != waitNone {
		t.Fatal("memory history prevented a CRTC event from ending EBUSY")
	}
	if l.nomemAttempts != 1 || l.nomemSince != start {
		t.Fatal("busy event lost memory recovery summary")
	}
}

func TestFrameLifecycleNoMemThenBusyLimit(t *testing.T) {
	l := frameLifecycle{stuckAfter: 200 * time.Millisecond}
	start := time.Unix(100, 0)
	l.nomem(start)
	l.timeout(l.stuckAt, true)
	busyStart := start.Add(nomemRetryMin)
	for i := range 4 {
		l.busy(busyStart.Add(time.Duration(i) * busyRetry))
		want := timeoutRetry
		if i == 3 {
			want = timeoutBusy
		}
		if got := l.timeout(l.stuckAt, true); got != want {
			t.Fatalf("busy attempt %d: got %v want %v", i+1, got, want)
		}
	}
	if l.nomemAttempts != 1 || l.nomemSince != start {
		t.Fatal("busy timeout lost memory recovery summary")
	}
}
