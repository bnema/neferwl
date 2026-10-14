package wayland

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// Pushing a capture reply and applying it on the display goroutine allocates
// nothing in steady state. The count includes the display goroutine:
// AllocsPerRun reads the global malloc counter.
func TestCapturedForwardAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the purego wake callback allocate")
	}
	s, _, _, _, _, _ := presentServer(t)
	done := ports.CaptureDone{ID: 77, Output: "HEADLESS-1", Time: time.Unix(1, 2)}
	// Posts run in order: once the marker ran, the drain before it did. The
	// marker is bound once and the channel reused, so waiting allocates nothing.
	ran := make(chan struct{}, 1)
	marker := func() { ran <- struct{}{} }
	for range 4 {
		s.capturedRing.Push(done)
		s.display.Post(marker)
		<-ran
	}
	allocs := testing.AllocsPerRun(200, func() {
		s.capturedRing.Push(done)
		s.display.Post(marker)
		<-ran
	})
	if allocs != 0 {
		t.Errorf("push + drain: %.1f allocs, want 0", allocs)
	}
}

// A full ring blocks the push until the display goroutine drains it, and no
// value is lost or reordered.
func TestPostRingFullBlocksWithoutLoss(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	const limit, total = 4, 3*4 + 1
	var got []int // display goroutine only
	r := newPostRing(s.display, limit, func(v int) { got = append(got, v) })

	// Stall the display goroutine so nothing drains.
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled

	pushed := make(chan int, total)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range total {
			if !r.Push(i) {
				t.Errorf("push %d refused", i)
				return
			}
			pushed <- i
		}
	}()
	for range limit {
		select {
		case <-pushed:
		case <-time.After(2 * time.Second):
			t.Fatal("pushes below the limit blocked")
		}
	}
	select {
	case i := <-pushed:
		t.Fatalf("push %d returned on a full ring", i)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	// The marker runs after every drain posted before it.
	var final []int
	ran := make(chan struct{})
	for {
		s.display.Post(func() {
			if len(got) == total {
				final = append(final, got...)
			}
			ran <- struct{}{}
		})
		<-ran
		if final != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i, v := range final {
		if v != i {
			t.Fatalf("applied %v, want 0..%d in order", final, total-1)
		}
	}
}

// A push blocked on a full ring resumes once the display drains it.
func TestPostRingBlockedPushResumes(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	r := newPostRing(s.display, 1, func(int) {})
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled
	if !r.Push(1) {
		t.Fatal("first push refused")
	}
	result := make(chan bool, 1)
	go func() { result <- r.Push(2) }()
	select {
	case ok := <-result:
		t.Fatalf("push on a full ring returned %v", ok)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("push refused while the display runs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked push never resumed")
	}
}

// A push blocked on a full ring returns false once the display has stopped,
// instead of waiting for a drain that never comes.
func TestPostRingPushStopsWithDisplay(t *testing.T) {
	s, err := New(Options{RuntimeDir: t.TempDir()}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	<-done
	r := newPostRing(s.display, 1, func(int) {})
	if !r.Push(1) { // room: queued, its drain is dropped by the stopped display
		t.Fatal("first push refused")
	}
	result := make(chan bool, 1)
	go func() { result <- r.Push(2) }()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("push on a stopped display reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked after the display stopped")
	}
}
