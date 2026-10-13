package wayland

import (
	"runtime"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/fifo"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

// Flips pushed while the display goroutine is busy are paced by one drain:
// one tick, one burst of frame callbacks per output, not one per stale flip.
func TestPresentationPushCoalescesWhileDisplayBusy(t *testing.T) {
	s, _, _, _, sink, _ := presentServer(t)
	release, started := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	go func() {
		s.display.Do(func() { close(started); <-release })
		close(finished)
	}()
	<-started
	for i := range 5 {
		sink.Push(ports.OutputPresented{Output: "HEADLESS-1", Flip: &ports.FlipInfo{Seq: uint64(i + 1), HardwareClock: true}})
	}
	close(release)
	<-finished
	var drains, seq uint64
	var reported bool
	s.display.Do(func() {
		drains = s.sink.drains.Load()
		seq = s.flips["HEADLESS-1"].Seq
		_, reported = s.reports["HEADLESS-1"]
	})
	if drains != 1 {
		t.Fatalf("%d drains for 5 flips pushed while busy, want 1", drains)
	}
	if !reported || seq != 5 {
		t.Fatalf("last flip %d, reported %v: every flip must still be answered in order", seq, reported)
	}
}

// A full ring merges the oldest flip of an output into its next one.
func TestPresentationRingMergesPastBound(t *testing.T) {
	p := &presentSink{ring: make([]ports.OutputPresented, 0, maxPresentRing)}
	for i := range maxPresentRing {
		p.ring = append(p.ring, ports.OutputPresented{Output: "A", Flip: &ports.FlipInfo{Seq: uint64(i + 1)}})
	}
	p.mu.Lock()
	p.dropOldestLocked()
	p.mu.Unlock()
	if len(p.ring) != maxPresentRing-1 || p.ring[0].Flip.Seq != 2 || p.ring[0].Flip.Merged != 1 {
		t.Fatalf("ring %d, first seq %d merged %d", len(p.ring), p.ring[0].Flip.Seq, p.ring[0].Flip.Merged)
	}
}

// Pushing a report and draining it on the display goroutine allocates nothing
// in steady state. The count includes the display goroutine: AllocsPerRun
// reads the global malloc counter.
func TestPresentationPushAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the purego wake callback allocate")
	}
	s, _, _, _, sink, _ := presentServer(t)
	r := ports.OutputPresented{Output: "HEADLESS-1"}
	for range 4 {
		sink.Push(r)
		s.display.Do(func() {})
	}
	allocs := testing.AllocsPerRun(200, func() {
		want := s.sink.drains.Load() + 1
		sink.Push(r)
		for s.sink.drains.Load() < want { // wait for the drain without allocating
			runtime.Gosched()
		}
	})
	if allocs != 0 {
		t.Errorf("push + drain: %.1f allocs, want 0", allocs)
	}
}

// A fifo barrier on an output that does not flip clears at barrierAt+period,
// driven by the pace timerfd: the next frame is not late by a timer tick.
func TestPaceTimerfdClearsBarrierOnDeadline(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "wp_fifo_manager_v1")
	f := c.AllocateID()
	registerProtocol(t, c, f)
	requestProtocol(t, c, manager, fifo.WpFifoManagerV1RequestGetFifo, f, surf)
	buf := shmBuffer(t, c)
	drainContents(contents)

	requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var barrierAt time.Time
	var period time.Duration
	s.display.Do(func() {
		root := s.windows[w.ID].xdg.surface
		barrierAt, period = root.barrierAt, s.outputPeriod(s.fifoOutput(root))
	})
	nextContent(t, contents, w.ID) // the first commit
	late := nextContent(t, contents, w.ID).Sub(barrierAt.Add(period))
	if late < -time.Millisecond || late > 2*time.Millisecond {
		t.Fatalf("queued commit applied %v after barrierAt+%v", late, period)
	}
}
