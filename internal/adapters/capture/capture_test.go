package capture

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

func TestFailClosesBeforeReplyAndUsesMonotonic(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan ports.CaptureDone, 1)
	Fail(context.Background(), ports.CaptureRequest{Dst: ports.SHMBuffer{File: f}}, nil, replies)
	done := <-replies
	if _, err := f.Stat(); err == nil {
		t.Fatal("fd open at reply")
	}
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	if d := time.Unix(now.Sec, now.Nsec).Sub(done.Time); d < 0 || d > time.Second {
		t.Fatalf("non-monotonic capture time: %v", d)
	}
}

func TestFailCancelledStillDeliversReadyReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replies := make(chan ports.CaptureDone, 1)
	for id := range uint64(100) {
		f, err := os.CreateTemp(t.TempDir(), "capture")
		if err != nil {
			t.Fatal(err)
		}
		Fail(ctx, ports.CaptureRequest{ID: id, Dst: ports.SHMBuffer{File: f}}, nil, replies)
		select {
		case result := <-replies:
			if result.ID != id {
				t.Fatalf("reply ID %d, want %d", result.ID, id)
			}
		default:
			t.Fatal("cancelled output lost ready completion")
		}
		if _, err := f.Stat(); err == nil {
			t.Fatal("descriptor open at reply")
		}
	}
}

func TestFailCancelledWithFullReplies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replies := make(chan ports.CaptureDone, 1)
	replies <- ports.CaptureDone{}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { Fail(ctx, ports.CaptureRequest{Dst: ports.SHMBuffer{File: f}}, nil, replies); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("fd still open")
	}
}
