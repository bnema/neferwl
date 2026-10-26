package drm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestMasterRetryBackoff(t *testing.T) {
	var m masterRetry
	now := time.Unix(0, 0)
	if _, ok := m.next(now); ok {
		t.Fatal("retry due before a refusal")
	}
	if m.refused(unix.EBUSY, false, now); m.pending {
		t.Fatal("a busy refusal is retried by the busy path, not here")
	}
	if m.refused(unix.EACCES, true, now); m.pending {
		t.Fatal("an enabled output is not retried")
	}
	m.refused(unix.EACCES, false, now)
	// The deadline is fixed: asking later leaves less, never more.
	if d, ok := m.next(now); !ok || d != masterRetryFirst {
		t.Fatalf("first retry %v %v", d, ok)
	}
	if d, _ := m.next(now.Add(masterRetryFirst / 2)); d != masterRetryFirst/2 {
		t.Fatalf("half-way %v", d)
	}
	if d, _ := m.next(now.Add(time.Hour)); d != 0 {
		t.Fatalf("overdue %v", d)
	}
	m.fire()
	if _, ok := m.next(now); ok {
		t.Fatal("pending while an attempt runs")
	}
	m.attempts = 30
	m.refused(unix.EPERM, false, now)
	if d, _ := m.next(now); d != masterRetryMax {
		t.Fatalf("capped retry %v", d)
	}
	m.attempts = masterRetryLimit
	if !m.refused(unix.EACCES, false, now) || m.pending {
		t.Fatal("retries past the limit")
	}
	m.done()
	if _, ok := m.next(now); ok || m.attempts != 0 {
		t.Fatal("retry after done")
	}
}

// retryHarness runs an output whose enable modesets the kernel refuses
// (lost master) until allow is called.
type retryHarness struct {
	o        *Output
	active   chan bool
	modesets func() int
	allow    func()
	stop     func()
}

func newRetryHarness(t *testing.T) retryHarness {
	t.Helper()
	var mu sync.Mutex
	refuse := true
	o, k, commits, commitMu := testOutputRules(t, func(_ *atomicReq, flags uint32) error {
		mu.Lock()
		defer mu.Unlock()
		if refuse && flags&atomicAllowModes != 0 && flags&atomicTestOnly == 0 {
			return unix.EACCES
		}
		return nil
	})
	o.cursor, o.tearing = nil, false
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(mock.Anything).Return().Maybe()
	r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	r.EXPECT().Close().Return().Once()
	// The 10 s stats tick runs in long tests.
	r.EXPECT().Trim(mock.Anything).Return(nil).Maybe()
	r.EXPECT().TakeRedrawn().Return(0).Maybe()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	o.ping = make(chan chan struct{})
	active := make(chan bool, 1)
	active <- false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, nil, discardSink(t), nil, nil)
	}()
	synctest.Wait()
	return retryHarness{
		o: o, active: active,
		modesets: func() int {
			commitMu.Lock()
			defer commitMu.Unlock()
			n := 0
			for _, c := range *commits {
				if c.flags&atomicAllowModes != 0 && c.flags&atomicTestOnly == 0 {
					n++
				}
			}
			return n
		},
		allow: func() { mu.Lock(); refuse = false; mu.Unlock() },
		stop: func() {
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		},
	}
}

// The seat reports active before the kernel gives DRM master back: the
// enable modeset is refused, then retried until it succeeds, without a
// second seat change, even while other events (watchdog pings) keep
// waking the loop faster than the backoff.
func TestEnableRefusedForMasterIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newRetryHarness(t)
		h.active <- true
		synctest.Wait()
		if n := h.modesets(); n != 1 {
			t.Fatalf("%d modesets after the refused enable", n)
		}
		// Let the backoff grow past the ping period.
		pingFor := func(d time.Duration) {
			for range int(d / (100 * time.Millisecond)) {
				time.Sleep(100 * time.Millisecond)
				h.o.Ping(context.Background())
			}
			synctest.Wait()
		}
		pingFor(3 * time.Second)
		before := h.modesets()
		if before < 6 {
			t.Fatalf("%d modesets in 3 s of pings: the retry was pushed back", before)
		}
		h.allow()
		pingFor(2 * masterRetryMax)
		after := h.modesets()
		if after != before+1 {
			t.Fatalf("%d modesets before success, %d after", before, after)
		}
		// Once enabled, nothing retries any more.
		pingFor(2 * masterRetryMax)
		if n := h.modesets(); n != after {
			t.Fatalf("%d modesets after success", n)
		}
		h.stop()
	})
}

// A seat disable ends a pending retry: no modeset may follow the plane
// reset before the next enable.
func TestSeatDisableCancelsMasterRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newRetryHarness(t)
		h.active <- true
		synctest.Wait()
		ack := make(chan struct{})
		h.o.seatDisable <- ack
		<-ack
		synctest.Wait()
		n := h.modesets()
		time.Sleep(10 * masterRetryMax)
		synctest.Wait()
		if got := h.modesets(); got != n {
			t.Fatalf("%d modesets after the seat disable, want %d", got, n)
		}
		h.stop()
	})
}
