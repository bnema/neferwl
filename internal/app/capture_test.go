package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCaptureUnknownOutputClosesFile(t *testing.T) {
	replies := make(chan ports.CaptureDone, 1)
	set := newOutputSet(context.Background(), replies)
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	set.routeCapture(ports.CaptureRequest{ID: 9, Output: "missing", Dst: ports.SHMBuffer{File: f}})
	select {
	case result := <-replies:
		if result.ID != 9 || result.Err == nil {
			t.Fatal(result)
		}
		if _, err := f.Stat(); err == nil {
			t.Fatal("request descriptor still open")
		}
	case <-time.After(time.Second):
		t.Fatal("no reply")
	}
}

func TestCaptureFullOutputQueueDoesNotBlockRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := make(chan ports.CaptureDone, 1)
	set := newOutputSet(ctx, replies)
	queued := make(chan ports.CaptureRequest, 1)
	queued <- ports.CaptureRequest{ID: 1}
	set.outs["test"] = &runningOutput{ctx: ctx, captures: queued, done: make(chan struct{}), stop: func() {}}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	routed := make(chan struct{})
	go func() {
		set.routeCapture(ports.CaptureRequest{ID: 2, Output: "test", Dst: ports.SHMBuffer{File: f}})
		close(routed)
	}()
	select {
	case <-routed:
	case <-time.After(time.Second):
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
}

func TestStoppedOutputCaptureStillReplies(t *testing.T) {
	ctx := context.Background()
	outputCtx, stop := context.WithCancel(ctx)
	stop()
	replies := make(chan ports.CaptureDone, 1)
	set := newOutputSet(ctx, replies)
	set.outs["test"] = &runningOutput{ctx: outputCtx, captures: make(chan ports.CaptureRequest)}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	set.routeCapture(ports.CaptureRequest{ID: 7, Output: "test", Dst: ports.SHMBuffer{File: f}})
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
	outputCtx, stop := context.WithCancel(ctx)
	stop()
	replies := make(chan ports.CaptureDone, 1)
	set := newOutputSet(ctx, replies)
	queued := make(chan ports.CaptureRequest, 1)
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	queued <- ports.CaptureRequest{ID: 8, Dst: ports.SHMBuffer{File: f}}
	set.failQueued(&runningOutput{ctx: outputCtx, captures: queued})
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
	pending := make(chan ports.CaptureRequest, 3)
	for i := 0; i < 3; i++ {
		f, err := os.CreateTemp(t.TempDir(), "queued")
		if err != nil {
			t.Fatal(err)
		}
		pending <- ports.CaptureRequest{Dst: ports.SHMBuffer{File: f}}
		t.Cleanup(func() {
			if _, err := f.Stat(); err == nil {
				t.Error("queued fd not closed")
			}
		})
	}
	drainCaptures(ctx, pending, nil)
	if len(pending) != 0 {
		t.Fatal("requests still queued")
	}
}

func TestOutputStopWithFullReplies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	replies := make(chan ports.CaptureDone, 1)
	replies <- ports.CaptureDone{}
	set := newOutputSet(ctx, replies)
	set.start(ctx, "test", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, requests <-chan ports.CaptureRequest) error {
		<-ctx.Done()
		return nil
	})
	f, err := os.CreateTemp(t.TempDir(), "queued")
	if err != nil {
		t.Fatal(err)
	}
	set.routeCapture(ports.CaptureRequest{Output: "test", Dst: ports.SHMBuffer{File: f}})
	cancel()
	done := make(chan struct{})
	go func() { _ = set.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("output teardown blocked on replies")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("queued fd still open")
	}
}
