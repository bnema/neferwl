package wayland

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/fifo"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A cached child buffer is not released until its role is destroyed;
// nothing in the output could have sampled an unpublished commit.
func TestCachedSubsurfaceBufferReleaseOnRoleDestroy(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	b := shmBuffer(t, c)
	p := &syncReleaseProxy{released: make(chan struct{}, 4)}
	p.SetID(b)
	registerWireProxy(c, p)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.released:
		t.Fatal("cached child buffer released before parent commit")
	case <-time.After(40 * time.Millisecond):
	}
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.released:
	case <-time.After(2 * time.Second):
		t.Fatal("dropped child buffer never released")
	}
}

func TestKeepHeld(t *testing.T) {
	now := time.Unix(100, 0)
	dma := heldBuffer{window: 1, id: 7, after: 5, at: now}
	shm := heldBuffer{window: 1, after: 5, at: now}
	out := func(shown, queued, seq uint64) ports.OutputPresented {
		return ports.OutputPresented{Shown: shown, Queued: queued, Seen: map[ports.WindowID]uint64{1: seq}}
	}
	type R = []ports.OutputPresented
	for _, tc := range []struct {
		name    string
		h       heldBuffer
		reports R
		after   time.Duration
		want    bool
	}{
		{"no report yet", dma, nil, 0, true},
		{"no report times out", dma, nil, heldTimeout, false},
		{"output has only this content", dma, R{out(0, 0, 5)}, 0, true},
		{"later content seen", dma, R{out(0, 0, 6)}, 0, false},
		{"scanned out", dma, R{out(7, 0, 6)}, time.Second, true},
		{"queued for scanout", dma, R{out(0, 7, 6)}, time.Second, true},
		{"another buffer scanned out", dma, R{out(8, 0, 6)}, 0, false},
		{"silent output times out", dma, R{out(0, 0, 5)}, heldTimeout, false},
		{"shm follows content", shm, R{out(0, 0, 6)}, 0, false},
		// A window moved off output A: A still scans the buffer out.
		{"other output scans it out", dma, R{out(0, 0, 6), out(7, 0, 6)}, time.Second, true},
		{"other output lags", dma, R{out(0, 0, 6), out(0, 0, 5)}, 0, true},
		{"all outputs moved on", dma, R{out(0, 0, 6), out(0, 0, 6)}, 0, false},
	} {
		if got := keepHeld(tc.h, tc.reports, now.Add(tc.after)); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// Child reads on a separate device outlive the stale display report timeout.
// The same predicate protects wl_buffer.release and explicit release points.
func TestKeepHeldChildReadsBeyondTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	report := ports.OutputPresented{
		Seen:       map[ports.WindowID]uint64{1: 20, 2: 20},
		ChildReads: map[ports.WindowID]uint64{1: 5},
	}
	for _, id := range []uint64{0, 7} { // SHM / explicit points only, and dmabuf
		h := heldBuffer{window: 1, id: id, after: 5, at: now}
		if !keepHeld(h, []ports.OutputPresented{report}, now.Add(time.Second)) {
			t.Fatal("unfinished child read released after 100ms")
		}
		h.window = 2
		if keepHeld(h, []ports.OutputPresented{report}, now.Add(time.Second)) {
			t.Fatal("unrelated display window held by child")
		}
		h.window, h.after = 1, 4
		if keepHeld(h, []ports.OutputPresented{report}, now.Add(time.Second)) {
			t.Fatal("buffer older than child's content held")
		}
		h.after = 5
		finished := report
		finished.ChildReads = nil // render completed, or device closed
		if keepHeld(h, []ports.OutputPresented{finished}, now.Add(time.Second)) {
			t.Fatal("finished child read still holds buffer")
		}
	}
	allocs := testing.AllocsPerRun(100, func() {
		keepHeld(heldBuffer{window: 1, after: 5, at: now}, []ports.OutputPresented{report}, now.Add(time.Second))
	})
	if allocs != 0 {
		t.Fatalf("child release guard: %.1f allocs, want 0", allocs)
	}
	t.Logf("child release guard: %.1f allocs", allocs)
}

// An actual unfinished child fence protects releases after the stale timeout,
// even when the display's Seen is unchanged (so no cap was necessary).
func TestKeepHeldUntilChildFenceCompletes(t *testing.T) {
	read, write, err := os.Pipe()
	require.NoError(t, err)
	defer write.Close()
	child := portsmocks.NewMockRenderer(t)
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(read, nil).Once()
	// A failed readback does not finish the render's client-buffer reads.
	child.EXPECT().BeginCapture().Return(nil, errors.New("readback unavailable")).Once()
	child.EXPECT().Close().Return().Once()
	p := capture.NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	s := ports.Scene{
		Capture:      &ports.SceneCapture{Workspace: 9},
		CaptureScene: &ports.Scene{OutputWidth: 2, OutputHeight: 2, Scale: 1, Windows: []ports.SceneWindow{{ID: 1}}},
	}
	p.SubmitHidden(s, map[ports.WindowID]ports.SurfaceContent{1: {Seq: 5}}, []ports.CaptureRequest{{
		ID: 1, Workspace: 9, OffScreen: true, Region: image.Rect(0, 0, 2, 2),
	}})
	now := time.Unix(100, 0)
	h := heldBuffer{window: 1, after: 5, at: now}
	seen := map[ports.WindowID]uint64{1: 5}
	got, reads, pending := p.CapHiddenSeen(seen)
	require.True(t, pending)
	require.Equal(t, seen, got)
	reports := []ports.OutputPresented{{Seen: got, ChildReads: reads}}
	require.True(t, keepHeld(h, reports, now.Add(time.Second)), "unsignalled fence outlives 100ms timeout")
	_, err = write.Write([]byte{1})
	require.NoError(t, err)
	got, reads, pending = p.CapHiddenSeen(seen)
	require.False(t, pending)
	reports[0] = ports.OutputPresented{Seen: got, ChildReads: reads}
	require.False(t, keepHeld(h, reports, now.Add(time.Second)), "signalled fence lifts child hold")
}

// A buffer replaced just after its window becomes invisible is still held:
// a scanout plane may read it until the flip that hides the window.
func TestInvisibleWindowBufferHeldUntilReport(t *testing.T) {
	s, events, commands, contents, dir := contentServer(t)
	go func() {
		for range contents {
		}
	}()
	c := protocolClient(t, s, dir)
	w, root, xdg := surfaceMapper(t, c, events)()
	// Our configures must not block the mapper's one-slot proxy.
	registerProtocol(t, c, xdg)
	// Commands apply asynchronously: wait for each configure.
	configure := func(v ports.ConfigureWindow) {
		t.Helper()
		commands <- v
		deadline := time.Now().Add(2 * time.Second)
		for {
			applied := false
			s.display.Do(func() { win := s.windows[w.ID]; applied = win.hasLast && win.last == v })
			if applied {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("configure not applied: %+v", v)
			}
			time.Sleep(time.Millisecond)
		}
	}
	configure(ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1", Visible: true})
	b, other := shmBuffer(t, c), shmBuffer(t, c)
	attach := func(buf uint32) {
		requestProtocol(t, c, root, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	}
	attach(b)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	configure(ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1"})
	attach(other)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		held := false
		for _, h := range s.held {
			held = held || h.res != nil && h.res.ID() == b
		}
		if !held {
			t.Errorf("replaced buffer of an invisible window released at once: held=%d", len(s.held))
		}
		// Without a report showing it, the hold still ends.
		s.releaseHeld(time.Now().Add(heldTimeout), nil)
		for _, h := range s.held {
			if h.res != nil && h.res.ID() == b {
				t.Error("held buffer of an invisible window never released")
			}
		}
	})
}

// A later queued use of the same wl_buffer still owns it after the output
// acknowledges the intervening commit.
func TestHeldBufferRequeuedBeforeOutputAck(t *testing.T) {
	s, events, commands, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1", Visible: true}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	manager := bindProtocol(t, c, "wp_fifo_manager_v1")
	f := c.AllocateID()
	registerProtocol(t, c, f)
	requestProtocol(t, c, manager, fifo.WpFifoManagerV1RequestGetFifo, f, root)
	b, other := shmBuffer(t, c), shmBuffer(t, c)
	p := &syncReleaseProxy{released: make(chan struct{}, 4)}
	p.SetID(b)
	registerWireProxy(c, p)
	attach := func(buf uint32) {
		requestProtocol(t, c, root, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	}
	attach(b)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	drainContents(contents)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
	attach(other)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
	attach(b)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		surf := s.windows[w.ID].xdg.surface
		if len(surf.queue) != 1 || surf.queue[0].buffer == nil || sameBuffer(surf.queue[0].buffer, surf.current) || len(s.held) == 0 {
			t.Errorf("expected replaced buffer and queued reuse: queue=%d held=%d", len(surf.queue), len(s.held))
		}
		s.reports["HEADLESS-1"] = ports.OutputPresented{Output: "HEADLESS-1", Seen: map[ports.WindowID]uint64{w.ID: ^uint64(0)}}
		s.releaseHeld(time.Now(), nil)
		if len(s.held) == 0 {
			t.Error("released held buffer despite queued reuse")
		}
		surf.barrier = false
		s.tickFifo(time.Now(), nil)
		s.releaseHeld(time.Now().Add(heldTimeout), nil)
		if len(s.held) != 0 {
			t.Error("held buffer not released after queued reuse applied")
		}
	})
	select {
	case <-p.released:
		t.Fatal("queued buffer released after output ack")
	case <-time.After(40 * time.Millisecond):
	}
}
