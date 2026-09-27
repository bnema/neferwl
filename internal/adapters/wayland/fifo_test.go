package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/committiming"
	"github.com/bnema/purego-libwayland/protocol/fifo"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// nextContent waits for a content of window id.
func nextContent(t *testing.T, contents <-chan ports.SurfaceContent, id ports.WindowID) time.Time {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-contents:
			if c.ID == id && !c.Empty() {
				return time.Now()
			}
		case <-deadline:
			t.Fatal("no content")
		}
	}
}

// shmBuffer creates a 1x1 wl_shm buffer.
func shmBuffer(t *testing.T, c *wlturbo.Display) uint32 {
	t.Helper()
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("fifo-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	return buffer
}

func drainContents(contents <-chan ports.SurfaceContent) {
	for {
		select {
		case <-contents:
		case <-time.After(20 * time.Millisecond):
			return
		}
	}
}

func TestFifoBarrierHoldsCommitOneRefresh(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "wp_fifo_manager_v1")
	f := c.AllocateID()
	registerProtocol(t, c, f)
	requestProtocol(t, c, manager, fifo.WpFifoManagerV1RequestGetFifo, f, surf)
	buf := shmBuffer(t, c)
	drainContents(contents)

	// Vulkan FIFO: every frame sets a barrier and waits on the last one.
	// Headless outputs never flip: the barrier clears after one 60 Hz
	// refresh, so three frames take at least two refreshes.
	start := time.Now()
	for range 3 {
		requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
		requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
		requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		nextContent(t, contents, w.ID)
	}
	if d := time.Since(start); d < 2*defaultFramePeriod-2*time.Millisecond {
		t.Fatalf("three fifo frames in %v", d)
	}
}

func TestCommitTimingDelaysCommit(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "wp_commit_timing_manager_v1")
	timer := c.AllocateID()
	registerProtocol(t, c, timer)
	requestProtocol(t, c, manager, committiming.WpCommitTimingManagerV1RequestGetTimer, timer, surf)
	buf := shmBuffer(t, c)
	drainContents(contents)

	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	target := time.Duration(now.Nano()) + 100*time.Millisecond
	start := time.Now()
	sec := uint64(target / time.Second)
	requestProtocol(t, c, timer, committiming.WpCommitTimerV1RequestSetTimestamp, uint32(sec>>32), uint32(sec), uint32(target%time.Second))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// Applied one refresh before the target, so it shows at the target.
	at := nextContent(t, contents, w.ID).Sub(start)
	if at < 100*time.Millisecond-defaultFramePeriod-2*time.Millisecond || at > time.Second {
		t.Fatalf("timed commit applied after %v", at)
	}

	// A second timestamp before the commit is an error.
	requestProtocol(t, c, timer, committiming.WpCommitTimerV1RequestSetTimestamp, uint32(0), uint32(1), uint32(0))
	requestProtocol(t, c, timer, committiming.WpCommitTimerV1RequestSetTimestamp, uint32(0), uint32(1), uint32(0))
	expectProtocolError(t, c, timer, uint32(committiming.WpCommitTimerV1ErrorTimestampExists))
}

// Far or past timestamps never hold a surface longer than a second.
func TestMonotonicTimeClamps(t *testing.T) {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sec    uint64
		nsec   int64
		lo, hi time.Duration
	}{
		{^uint64(0), 0, maxTimestampAhead - 10*time.Millisecond, maxTimestampAhead},
		{0, 0, -10 * time.Millisecond, 10 * time.Millisecond},
		{uint64(now.Sec), now.Nsec, -10 * time.Millisecond, 10 * time.Millisecond},
	} {
		got := time.Until(monotonicTime(tc.sec, tc.nsec))
		if got < tc.lo || got > tc.hi {
			t.Errorf("sec %d: %v not in [%v, %v]", tc.sec, got, tc.lo, tc.hi)
		}
	}
}

// A barrier on an output that flips clears on its next flip.
func TestTickFifoClearsBarrierOnFlip(t *testing.T) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{out}, fifoSurfaces: map[*surface]struct{}{}, lastFlip: map[string]time.Time{}, frameReady: make(chan struct{}, 1)}
	surf := &surface{server: s, xdg: &xdgSurface{window: &window{last: ports.ConfigureWindow{Output: "DP-2"}}}}
	now := time.Unix(100, 0)
	s.lastFlip["DP-2"] = now.Add(-time.Millisecond)
	surf.setBarrier(now)
	if _, waiting := s.tickFifo(now.Add(time.Millisecond), nil); !waiting || !surf.barrier {
		t.Fatal("barrier cleared before the flip")
	}
	// A flipping output waits up to 1.5 refreshes for a missed flip.
	if w, _ := s.tickFifo(now.Add(20*time.Millisecond), nil); !surf.barrier || w < 4*time.Millisecond {
		t.Fatalf("barrier %v wait %v", surf.barrier, w)
	}
	if _, waiting := s.tickFifo(now.Add(21*time.Millisecond), map[string]bool{"DP-2": true}); waiting || surf.barrier {
		t.Fatal("barrier kept after the flip")
	}
}

// A queued commit whose buffer is destroyed (a swapchain resize) is skipped
// without unmapping the window.
func TestFifoQueuedBufferDestroyed(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "wp_fifo_manager_v1")
	f := c.AllocateID()
	registerProtocol(t, c, f)
	requestProtocol(t, c, manager, fifo.WpFifoManagerV1RequestGetFifo, f, surf)
	buf := shmBuffer(t, c)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	// Queued behind the barrier, then its buffer goes away.
	requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, buf, wayland.BufferRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	drainContents(contents)
	time.Sleep(3 * defaultFramePeriod)
	for {
		select {
		case ev := <-events:
			if _, ok := ev.(ports.WindowUnmapped); ok {
				t.Fatal("window unmapped by a dead queued buffer")
			}
		default:
			return
		}
	}
}

// A parent can keep a dependency after the child's queue is dropped or its
// update is applied independently. Neither path may recycle that dependency
// into a new commit while the parent still holds it.
func TestQueuedParentRetainsRetiredChild(t *testing.T) {
	for _, dropped := range []bool{true, false} {
		name := "applied"
		if dropped {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) {
			srv := &Server{fifoSurfaces: make(map[*surface]struct{}), frameReady: make(chan struct{}, 1)}
			parent := &surface{server: srv}
			child := &surface{server: srv}
			child.sub.parent, child.sub.synced = parent, true
			parent.sub.children = []*surface{child}
			child.pendingScale = 2
			child.queueUpdate()
			old := child.queue[0]
			parent.pendingScale = 3
			parent.queueUpdate()
			pu := parent.queue[0]
			if old.refs != 1 || len(pu.deps) != 1 || pu.deps[0] != old {
				t.Fatal("parent did not capture child's update")
			}
			if dropped {
				child.dropQueue()
			} else {
				old.applyGraph()
			}
			if !old.retired || old.refs != 1 {
				t.Fatalf("retired child ref count: retired=%v refs=%d", old.retired, old.refs)
			}
			child.pendingScale = 7
			child.queueUpdate()
			if child.queue[0] == old || old.scale != 2 || pu.deps[0] != old {
				t.Fatal("live dependency was recycled as a new child commit")
			}
			if !pu.graphReady(time.Now()) {
				t.Fatal("retired dependency kept parent waiting")
			}
			pu.applyGraph()
			if parent.bufferScale != 3 || len(parent.queue) != 0 || len(child.queue) != 1 || child.queue[0].scale != 7 {
				t.Fatal("parent replayed old dependency or changed new child commit")
			}
			child.dropQueue()
		})
	}
}

// Rechecking a shared predecessor in a traversal must reuse its readiness.
func TestGraphReadyMemoizesPredecessors(t *testing.T) {
	s := &Server{readinessGeneration: 1}
	surf := &surface{server: s}
	first := &update{owner: surf}
	second := &update{owner: surf}
	third := &update{owner: surf}
	surf.queue = []*update{first, second, third}
	second.prev = first
	third.prev = second
	now := time.Now()
	if !third.graphReady(now) {
		t.Fatal("ready chain rejected")
	}
	if first.readyGeneration != s.readinessGeneration || second.readyGeneration != s.readinessGeneration {
		t.Fatal("predecessors not memoized")
	}
}
