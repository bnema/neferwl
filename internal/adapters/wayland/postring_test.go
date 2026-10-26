package wayland

import (
	"context"
	"runtime"
	"strings"
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
	applied := make(chan struct{})
	r := newPostRing(s.display, limit, func(v int) {
		got = append(got, v)
		if len(got) == total {
			close(applied)
		}
	})

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
		case <-waylandDeadline(2 * time.Second):
			t.Fatal("pushes below the limit blocked")
		}
	}
	select {
	case i := <-pushed:
		t.Fatalf("push %d returned on a full ring", i)
	case <-waylandDeadline(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	select {
	case <-applied:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("ring did not apply all pushes")
	}
	final := got
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
	case <-waylandDeadline(50 * time.Millisecond):
	}
	close(release)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("push refused while the display runs")
		}
	case <-waylandDeadline(2 * time.Second):
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
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
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
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("push blocked after the display stopped")
	}
}

// A push that waits on a full ring reports how long, and a drain-side push
// that finds room does not.
func TestPostRingStatsPushBlock(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	r := newPostRing(s.display, 1, func(int) {})
	// A failed assertion still frees the display before server shutdown.
	release := releaseOnce(t)
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release.ch })
	<-stalled
	r.Push(1)
	if st := r.takeStats(); st.maxLen != 1 || st.maxPushBlock != 0 {
		t.Fatalf("stats = %+v, want maxLen 1 and no block", st)
	}
	done := make(chan struct{})
	go func() { defer close(done); r.Push(2) }() // full: waits for the drain
	waitPushWaiting(t)
	seen := time.Now()
	// Join a goroutine while the display remains stalled: this measured
	// interval is a real lower bound on the push's blocked duration.
	measured := make(chan struct{})
	go func() { close(measured) }()
	<-measured
	held := time.Since(seen)
	release.do()
	<-done
	if st := r.takeStats(); st.maxPushBlock < held {
		t.Fatalf("stats = %+v, want a push block of at least %v", st, held)
	}
}

// waitPushWaiting returns once a goroutine waits inside postRing.Push, read
// from the goroutine dump, so the test needs no sleep to know the push blocks.
func waitPushWaiting(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no goroutine waits in postRing.Push:\n%s", buf[:runtime.Stack(buf, true)])
		}
		dump := string(buf[:runtime.Stack(buf, true)])
		for _, g := range strings.Split(dump, "\n\n") {
			if strings.Contains(g, "[select") && strings.Contains(g, "postRing") && strings.Contains(g, ").Push(") {
				return
			}
		}
		runtime.Gosched()
	}
}

func TestPostRingStatsSampleStalledOccupancy(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled
	// Release even if an assertion fails, so server cleanup cannot deadlock.
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	r := newPostRing(s.display, 2, func(int) {})
	for i := range 2 {
		if !r.Push(i) {
			t.Fatal("push refused")
		}
	}
	for range 2 {
		if got := r.takeStats().maxLen; got != 2 {
			t.Fatalf("occupancy %d, want 2", got)
		}
	}
	close(release)
	released = true
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	if got := r.takeStats().maxLen; got != 0 {
		t.Fatalf("recovered occupancy %d", got)
	}
}
