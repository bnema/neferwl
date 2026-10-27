package routing

import (
	"context"
	"os"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

func TestCaptureUnknownOutputClosesFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replies := make(chan domain.CaptureDone, 1)
		router := NewRouter(context.Background(), replies, Routes{})
		f, err := os.CreateTemp(t.TempDir(), "capture")
		if err != nil {
			t.Fatal(err)
		}
		router.RouteCapture(domain.CaptureRequest{ID: 9, Output: "missing", Dst: domain.SHMBuffer{File: f}})
		synctest.Wait()
		select {
		case result := <-replies:
			if result.ID != 9 || result.Err == nil {
				t.Fatal(result)
			}
			if _, err := f.Stat(); err == nil {
				t.Fatal("request descriptor still open")
			}
		default:
			t.Fatal("no reply")
		}
	})
}

func TestCaptureFullOutputQueueDoesNotBlockRouting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		replies := make(chan domain.CaptureDone, 1)
		router := NewRouter(ctx, replies, Routes{})
		queued := make(chan domain.CaptureRequest, 1)
		queued <- domain.CaptureRequest{ID: 1}
		router.outs["test"] = &Mailbox{Ctx: ctx, Captures: queued, Done: make(chan struct{}), Stop: func() {}}
		f, err := os.CreateTemp(t.TempDir(), "capture")
		if err != nil {
			t.Fatal(err)
		}
		routed := make(chan struct{})
		go func() {
			router.RouteCapture(domain.CaptureRequest{ID: 2, Output: "test", Dst: domain.SHMBuffer{File: f}})
			close(routed)
		}()
		synctest.Wait()
		select {
		case <-routed:
		default:
			cancel()
			<-routed
			t.Fatal("full capture queue blocked output routing")
		}
		result := <-replies
		if result.ID != 2 || result.Err == nil {
			t.Fatalf("rejected capture: %+v", result)
		}
		if _, err := f.Stat(); err == nil {
			t.Fatal("rejected descriptor still open")
		}
		if got := <-queued; got.ID != 1 {
			t.Fatalf("queued request replaced: %+v", got)
		}
	})
}

func TestStoppedOutputCaptureStillReplies(t *testing.T) {
	ctx := context.Background()
	outputCtx, stop := context.WithCancel(ctx)
	stop()
	replies := make(chan domain.CaptureDone, 1)
	router := NewRouter(ctx, replies, Routes{})
	router.outs["test"] = &Mailbox{Ctx: outputCtx, Captures: make(chan domain.CaptureRequest)}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	router.RouteCapture(domain.CaptureRequest{ID: 7, Output: "test", Dst: domain.SHMBuffer{File: f}})
	select {
	case result := <-replies:
		if result.ID != 7 || result.Err == nil {
			t.Fatalf("stopped output reply: %+v", result)
		}
	default:
		t.Fatal("stopped output lost capture completion")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("stopped output kept descriptor open")
	}
}

func TestFinishedOutputDrainsCapturesWithReplies(t *testing.T) {
	ctx := context.Background()
	replies := make(chan domain.CaptureDone, 1)
	queued := make(chan domain.CaptureRequest, 1)
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	queued <- domain.CaptureRequest{ID: 8, Dst: domain.SHMBuffer{File: f}}
	failQueued(ctx, &Mailbox{Captures: queued}, replies)
	select {
	case result := <-replies:
		if result.ID != 8 || result.Err == nil {
			t.Fatalf("drained capture reply: %+v", result)
		}
	default:
		t.Fatal("drained capture lost completion")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("drained descriptor still open")
	}
}

func TestDrainCapturesClosesQueuedFDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pending := make(chan domain.CaptureRequest, 3)
	for i := 0; i < 3; i++ {
		f, err := os.CreateTemp(t.TempDir(), "queued")
		if err != nil {
			t.Fatal(err)
		}
		pending <- domain.CaptureRequest{Dst: domain.SHMBuffer{File: f}}
		t.Cleanup(func() {
			if _, err := f.Stat(); err == nil {
				t.Error("queued fd not closed")
			}
		})
	}
	DrainCaptures(ctx, pending, nil)
	if len(pending) != 0 {
		t.Fatal("requests still queued")
	}
}

func TestOutputStopWithFullReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		replies := make(chan domain.CaptureDone, 1)
		replies <- domain.CaptureDone{}
		captures := make(chan domain.CaptureRequest)
		set := NewSet(ctx, replies, Routes{Captures: captures})
		set.Start(ctx, "test", idleRun)
		f, err := os.CreateTemp(t.TempDir(), "queued")
		if err != nil {
			t.Fatal(err)
		}
		captures <- domain.CaptureRequest{Output: "test", Dst: domain.SHMBuffer{File: f}}
		cancel()
		done := make(chan struct{})
		go func() { _ = set.Wait(); close(done) }()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("output teardown blocked on replies")
		}
		if _, err := f.Stat(); err == nil {
			t.Fatal("queued fd still open")
		}
	})
}
