package drm

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestNoMemRetriesWithoutModeset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := startStuck(t, nil, unix.ENOMEM, unix.ENOMEM, nil)
		h.scenes <- ports.Scene{Seq: 1}
		synctest.Wait()
		for _, delay := range []time.Duration{nomemRetryMin, 2 * nomemRetryMin} {
			select {
			case p := <-h.presented:
				t.Fatalf("refused frame reported: %+v", p)
			default:
			}
			time.Sleep(delay)
			synctest.Wait()
		}
		c := h.snapshot()
		if len(c) != 5 {
			t.Fatalf("commits %d, want 5", len(c))
		}
		for _, r := range c[2:] {
			if r.flags&atomicAllowModes != 0 || r.user&3 != userFrame {
				t.Fatalf("not a frame retry: %+v", r)
			}
		}
		select {
		case p := <-h.presented:
			t.Fatalf("reported before successful flip: %+v", p)
		default:
		}
		h.flipped <- eventOf(c[4])
		synctest.Wait()
		select {
		case <-h.presented:
		default:
			t.Fatal("successful flip not reported")
		}
		select {
		case p := <-h.presented:
			t.Fatalf("extra report: %+v", p)
		default:
		}
		h.stop()
	})
}

func TestLastingNoMemKeepsOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		errs := make([]error, 21)
		for i := range errs {
			errs[i] = unix.ENOMEM
		}
		h := startStuck(t, nil, errs...)
		h.scenes <- ports.Scene{Seq: 1}
		synctest.Wait()
		for i := range 19 {
			time.Sleep(min(nomemRetryMax, nomemRetryMin*time.Duration(1<<min(i, 7))))
			synctest.Wait()
		}
		select {
		case err := <-h.done:
			t.Fatalf("Run stopped: %v", err)
		default:
		}
		c := h.stop()
		if len(c) != 22 {
			t.Fatalf("commits %d want 22", len(c))
		}
		for _, r := range c[2:] {
			if r.flags&atomicAllowModes != 0 {
				t.Fatal("ENOMEM triggered modeset")
			}
		}
	})
}

func TestNoMemModesetRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := startStuck(t, nil, nil, unix.ENOMEM, nil)
		h.scenes <- ports.Scene{Seq: 1, Off: true}
		synctest.Wait()
		if h.count() != 3 {
			t.Fatal("power off did not commit")
		}
		h.scenes <- ports.Scene{Seq: 2}
		synctest.Wait()
		time.Sleep(nomemRetryMin)
		synctest.Wait()
		select {
		case err := <-h.done:
			t.Fatalf("Run stopped: %v", err)
		default:
		}
		c := h.stop()
		if len(c) != 6 {
			t.Fatalf("commits %d want 6", len(c))
		}
		for _, r := range c[3:5] {
			if r.flags != atomicAllowModes {
				t.Fatalf("not a retried modeset: %#x", r.flags)
			}
		}
	})
}

func TestScanoutNoMemComposes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o, _, _ := testOutput(t, unix.ENOMEM)
		s, surfaces := fullscreenScene()
		c := surfaces[1]
		o.clientFBs[9] = &clientFB{fbID: 80}
		o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
		direct, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorBypass)
		if direct || err != nil || o.reason != "nomem" || o.clientFBs[9].failed != "" {
			t.Fatalf("direct=%v err=%v reason=%q failed=%q", direct, err, o.reason, o.clientFBs[9].failed)
		}
		if d := o.composeFrame(s, surfaces); d.overlay.fb != 0 || d.composed.Seq != s.Seq {
			t.Fatal("immediate fallback did not compose")
		}
		for _, delay := range []time.Duration{0, nomemScanoutPause - time.Nanosecond} {
			time.Sleep(delay)
			d := o.decideFrame(s, surfaces, false)
			if d.fb != 0 || d.overlay.fb != 0 || d.composed.Seq != s.Seq || o.reason != "nomem" {
				t.Fatalf("not composed: %+v reason=%q", d, o.reason)
			}
		}
		time.Sleep(time.Nanosecond)
		if d := o.decideFrame(s, surfaces, false); d.fb != 80 {
			t.Fatalf("scanout not resumed: %+v reason=%q", d, o.reason)
		}
	})
}

func TestProtectedNoMemKeepsProtectionRetry(t *testing.T) {
	for _, stage := range []string{"modeset", "commit"} {
		t.Run(stage, func(t *testing.T) {
			o, k, _ := testOutput(t, unix.ENOMEM)
			var state atomic.Uint64
			state.Store(3)
			securityGate(t, o, &state)
			o.observeSecurity()
			events := make(chan ports.SecurityBackendEvent, 4)
			o.SecurityEvents = events
			var err error
			if stage == "modeset" {
				k.EXPECT().createBlob(mock.Anything).Return(99, nil)
				k.EXPECT().destroyBlob(mock.Anything).Return(nil)
				err = o.modesetImage(70, true)
			} else {
				o.securityPrepared = true // an admitted lock-screen frame
				err = o.commitFrame(70, nil, false, false, pendingFrame{security: o.securityState})
			}
			enabled := true
			if !o.commitFailed(err, &enabled) || !enabled || o.securityPrepared || !o.protectBackoffActive() || !o.frame.nomemSince.IsZero() {
				t.Fatalf("protection retry bypassed: err=%v prepared=%v backoff=%v nomem=%v", err, o.securityPrepared, o.protectBackoff, o.frame.nomemSince)
			}
			select {
			case ev := <-events:
				t.Fatalf("proof on refusal: %T", ev)
			default:
			}
		})
	}
}

func TestNoMemSuccessClearsRun(t *testing.T) {
	o, _, _ := testOutput(t, unix.ENOMEM, nil)
	enabled := true
	if !o.commitFailed(o.commitFrame(70, nil, false, false, pendingFrame{}), &enabled) {
		t.Fatal("ENOMEM unhandled")
	}
	o.frame.timeout(o.frame.stuckAt, true)
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if !o.frame.nomemSince.IsZero() || o.frame.nomemAttempts != 0 {
		t.Fatal("successful commit did not clear run")
	}
}

func TestNoMemRetryRechecksSecurity(t *testing.T) {
	o, _, commits := testOutput(t, unix.ENOMEM)
	enabled := true
	if !o.commitFailed(o.commitFrame(70, nil, false, false, pendingFrame{}), &enabled) {
		t.Fatal("ENOMEM unhandled")
	}
	o.frame.timeout(o.frame.stuckAt, true)
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != errSecurityScene {
		t.Fatalf("stale retry: %v", err)
	}
	if len(*commits) != 1 {
		t.Fatal("stale retry reached KMS")
	}
}
