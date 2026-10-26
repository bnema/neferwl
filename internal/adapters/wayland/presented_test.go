package wayland

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/fifo"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// slowOutputs has two outputs refreshing once a second: their deadlines never
// fire inside a test, so only flips pace their frame callbacks.
var slowOutputs = ports.Layout{
	{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 1920, Height: 1080, RefreshMilli: 1000}, Width: 1920, Height: 1080, Scale: 1},
	{Info: ports.OutputInfo{Name: "HEADLESS-2", Width: 1920, Height: 1080, RefreshMilli: 1000}, X: 1920, Width: 1920, Height: 1080, Scale: 1},
}

// frameDoneProxy records the time of a wl_callback.done.
type frameDoneProxy struct {
	wlturbo.BaseProxy
	done chan uint32
}

func (p *frameDoneProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.CallbackEventDone) {
		p.done <- binary.NativeEndian.Uint32(e.Data())
	}
}

// requestFrame commits the surface with a new frame callback.
func requestFrame(t *testing.T, c *wlturbo.Display, surf uint32) *frameDoneProxy {
	t.Helper()
	cb := c.AllocateID()
	p := &frameDoneProxy{done: make(chan uint32, 2)}
	p.SetID(cb)
	registerWireProxy(c, p)
	requestProtocol(t, c, surf, wayland.SurfaceRequestFrame, cb)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return p
}

// awaitFrame waits for the callback to be answered and returns its time.
func awaitFrame(t *testing.T, c *wlturbo.Display, p *frameDoneProxy) uint32 {
	t.Helper()
	return recvWire(t, c, "frame done", p.done)
}

// placeWindow puts the window on an output and waits until wayland applied it.
func placeWindow(t *testing.T, s *Server, commands chan<- ports.ClientCommand, w ports.WindowMapped, output string) {
	t.Helper()
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: output, Visible: true}
	flushCommands(t, s, commands)
	var got string
	s.display.Do(func() { got = s.frameOutput(s.windows[w.ID].xdg.surface) })
	if got != output {
		t.Fatalf("window not placed on %s", output)
	}
}

// Flips pushed while the display goroutine is busy are paced by one tick per
// output, not one per stale flip. The frame callback of each output is
// answered once, with the time of that output's last flip: a tick per flip
// would have answered it with the first flip's time.
func TestPresentationPushCoalescesWhileDisplayBusy(t *testing.T) {
	s, events, commands, _, sink, dir := presentServerOn(t, slowOutputs)
	c := protocolClient(t, s, dir)
	mk := surfaceMapper(t, c, events)
	outputs := []string{"HEADLESS-1", "HEADLESS-2"}
	surfs := make([]uint32, len(outputs))
	for i, name := range outputs {
		w, surf, _ := mk()
		placeWindow(t, s, commands, w, name)
		surfs[i] = surf
		// The first callback of an idle output is answered at once and
		// starts its one-second period: the next one waits for a flip.
		awaitFrame(t, c, requestFrame(t, c, surf))
	}
	waiting := make([]*frameDoneProxy, len(outputs))
	for i := range outputs {
		waiting[i] = requestFrame(t, c, surfs[i])
		select {
		case ms := <-waiting[i].done:
			t.Fatalf("%s answered before any flip (at %d ms)", outputs[i], ms)
		default:
		}
	}
	var base time.Duration
	release, started, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		s.display.Do(func() { base = monotonic(s.started); close(started); <-release })
		close(finished)
	}()
	<-started
	const flips = 5
	const step = 100 * time.Millisecond
	for i := range flips {
		for _, name := range outputs {
			sink.Push(ports.OutputPresented{Output: name, Flip: &ports.FlipInfo{When: base + time.Duration(i+1)*step, Seq: uint64(i + 1), HardwareClock: true}})
		}
	}
	close(release)
	<-finished
	last := uint32((flips * step).Milliseconds())
	for i, name := range outputs {
		ms := awaitFrame(t, c, waiting[i])
		// base is read on the display goroutine a little before the server
		// answers: a few ms of slack, far below one step.
		if ms < last-5 || ms > last+5 {
			t.Fatalf("%s answered at %d ms, want the last flip's %d ms (the first flip is at %d)", name, ms, last, step.Milliseconds())
		}
	}
	var seq uint64
	var reported bool
	s.display.Do(func() {
		seq = s.flips["HEADLESS-1"].Seq
		_, reported = s.reports["HEADLESS-1"]
	})
	if !reported || seq != flips {
		t.Fatalf("last flip %d, reported %v: every flip must still be answered in order", seq, reported)
	}
}

func ringOf(outputs ...string) *presentSink {
	p := &presentSink{ring: make([]ports.OutputPresented, 0, maxPresentRing+1)}
	for _, o := range outputs {
		p.ring = append(p.ring, ports.OutputPresented{Output: o})
	}
	return p
}

// fill pads the ring with reports of distinct outputs up to the bound.
func (p *presentSink) fill() {
	for i := 0; len(p.ring) < maxPresentRing; i++ {
		p.ring = append(p.ring, ports.OutputPresented{Output: "pad" + string(rune('A'+i/26)) + string(rune('a'+i%26))})
	}
}

func flipOf(seq uint64, shows ...ports.WindowID) *ports.FlipInfo {
	f := &ports.FlipInfo{Seq: seq}
	if len(shows) > 0 {
		f.Shows = map[ports.WindowID]uint64{}
		for _, id := range shows {
			f.Shows[id] = seq
		}
	}
	return f
}

// makeRoomLocked on a full ring: which report it merges or moves, and what it
// reports.
func TestPresentationRingMakeRoom(t *testing.T) {
	tests := []struct {
		name       string
		ring       func() *presentSink
		in         ports.OutputPresented
		wantMerged bool
		check      func(t *testing.T, p *presentSink, in *ports.OutputPresented)
	}{
		{
			// A full ring merges the oldest flip of an output into its next flip.
			name: "merges past the bound",
			ring: func() *presentSink {
				p := ringOf()
				for i := range maxPresentRing {
					p.ring = append(p.ring, ports.OutputPresented{Output: "A", Flip: flipOf(uint64(i + 1))})
				}
				return p
			},
			in:         ports.OutputPresented{Output: "A", Flip: flipOf(maxPresentRing + 1)},
			wantMerged: true,
			check: func(t *testing.T, p *presentSink, _ *ports.OutputPresented) {
				if len(p.ring) != maxPresentRing-1 || p.ring[0].Flip.Seq != 2 || p.ring[0].Flip.Merged != 1 {
					t.Fatalf("ring %d, first seq %d merged %d", len(p.ring), p.ring[0].Flip.Seq, p.ring[0].Flip.Merged)
				}
			},
		},
		{
			// A flip followed by a flip-less report of the same output is not
			// lost: the later report carries it.
			name: "keeps a flip before a flip-less report",
			ring: func() *presentSink {
				p := ringOf("A", "A")
				p.ring[0].Flip = flipOf(7, 3)
				p.fill()
				p.ring = p.ring[:maxPresentRing]
				return p
			},
			in: ports.OutputPresented{Output: "B", Flip: flipOf(9)},
			check: func(t *testing.T, p *presentSink, _ *ports.OutputPresented) {
				if len(p.ring) != maxPresentRing-1 || p.ring[0].Output != "A" || p.ring[0].Flip == nil || p.ring[0].Flip.Seq != 7 || p.ring[0].Flip.Merged != 0 {
					t.Fatalf("flip lost: ring %d, first %+v", len(p.ring), p.ring[0])
				}
			},
		},
		{
			// With several outputs interleaved, the victim is the oldest report
			// that has a successor of its own output; other outputs' reports are
			// untouched.
			name: "interleaved outputs",
			ring: func() *presentSink {
				p := ringOf("A", "B", "A", "B")
				p.ring[0].Flip, p.ring[1].Flip = flipOf(1, 10), flipOf(2, 20)
				p.ring[2].Flip, p.ring[3].Flip = flipOf(3, 11), nil
				p.fill()
				return p
			},
			in:         ports.OutputPresented{Output: "C"},
			wantMerged: true,
			check: func(t *testing.T, p *presentSink, _ *ports.OutputPresented) {
				if p.ring[0].Output != "B" || p.ring[0].Flip.Seq != 2 || p.ring[0].Flip.Merged != 0 {
					t.Fatalf("B's flip touched: %+v", p.ring[0])
				}
				a := p.ring[1]
				if a.Output != "A" || a.Flip.Seq != 3 || a.Flip.Merged != 1 || a.Flip.Shows[10] != 1 || a.Flip.Shows[11] != 3 {
					t.Fatalf("A's flips not merged: %+v", a.Flip)
				}
				if p.ring[2].Output != "B" || p.ring[2].Flip != nil {
					t.Fatalf("B's flip-less report moved: %+v", p.ring[2])
				}
			},
		},
		{
			// The incoming report counts as the successor of a ring report.
			name: "incoming takes over a flip",
			ring: func() *presentSink {
				p := ringOf("A")
				p.ring[0].Flip = flipOf(4)
				p.fill()
				return p
			},
			in: ports.OutputPresented{Output: "A"},
			check: func(t *testing.T, p *presentSink, in *ports.OutputPresented) {
				if in.Flip == nil || in.Flip.Seq != 4 || len(p.ring) != maxPresentRing-1 {
					t.Fatalf("incoming %+v, ring %d", in.Flip, len(p.ring))
				}
			},
		},
		{
			// With no successor for any report (more outputs than ring slots)
			// nothing is dropped: the ring grows by one and every flip is still
			// delivered.
			name: "without a successor keeps everything",
			ring: func() *presentSink {
				p := ringOf()
				for i := range maxPresentRing {
					p.ring = append(p.ring, ports.OutputPresented{Output: "o" + string(rune('A'+i/26)) + string(rune('a'+i%26)), Flip: flipOf(uint64(i + 1))})
				}
				return p
			},
			in: ports.OutputPresented{Output: "other", Flip: flipOf(99)},
			check: func(t *testing.T, p *presentSink, _ *ports.OutputPresented) {
				if len(p.ring) != maxPresentRing {
					t.Fatalf("merged or dropped: ring %d", len(p.ring))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.ring()
			in := tt.in
			if merged := p.makeRoomLocked(&in); merged != tt.wantMerged {
				t.Fatalf("merged %v, want %v", merged, tt.wantMerged)
			}
			tt.check(t, p, &in)
		})
	}
}

// Merges are counted, and warned about at most once per mergeWarnEvery.
func TestPresentationRingWarnsRateLimited(t *testing.T) {
	now := time.Unix(1000, 0)
	clk := portsmocks.NewMockClock(t)
	clk.EXPECT().Now().RunAndReturn(func() time.Time { return now })
	p := ringOf()
	p.s = &Server{clock: clk}
	for i := range maxPresentRing {
		p.ring = append(p.ring, ports.OutputPresented{Output: "A", Flip: flipOf(uint64(i + 1))})
	}
	push := func(seq uint64) (bool, int) {
		return p.enqueueLocked(ports.OutputPresented{Output: "A", Flip: flipOf(seq)})
	}
	if warn, n := push(100); !warn || n != 1 {
		t.Fatalf("first merge: warn %v merged %d", warn, n)
	}
	for i := range 3 {
		now = now.Add(time.Second)
		if warn, _ := push(uint64(101 + i)); warn {
			t.Fatalf("warned again after %v", time.Duration(i+1)*time.Second)
		}
	}
	now = now.Add(mergeWarnEvery)
	if warn, n := push(200); !warn || n != 4 {
		t.Fatalf("after the interval: warn %v merged %d, want the 4 merges since", warn, n)
	}
}

// A stalled display loop makes outputs merge flips; wayland says so once.
func TestPresentationMergeWarnsInLog(t *testing.T) {
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
	s, _, _, _, _ := contentServerWith(t, Options{}, log)
	release, started, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		s.display.Do(func() { close(started); <-release })
		close(finished)
	}()
	<-started
	for i := range 3 * maxPresentRing {
		s.Presentation().Push(ports.OutputPresented{Output: "HEADLESS-1", Flip: &ports.FlipInfo{Seq: uint64(i + 1)}})
	}
	close(release)
	<-finished
	s.display.Do(func() {})
	got := out.String()
	if n := strings.Count(got, `"level":"warn"`); n != 1 {
		t.Fatalf("%d warnings: %s", n, got)
	}
	if !strings.Contains(got, `"component":"wayland"`) || !strings.Contains(got, `"merged":`) {
		t.Fatalf("warning lacks fields: %s", got)
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
	// Posts run in order: once the marker ran, the drain before it did.
	// The marker is bound once and the channel is reused, so waiting for it
	// allocates nothing.
	ran := make(chan struct{}, 1)
	marker := func() { ran <- struct{}{} }
	for range 4 {
		sink.Push(r)
		s.display.Post(marker)
		<-ran
	}
	allocs := testing.AllocsPerRun(200, func() {
		sink.Push(r)
		s.display.Post(marker)
		<-ran
	})
	if allocs != 0 {
		t.Errorf("push + drain: %.1f allocs, want 0", allocs)
	}
}

// A fifo barrier on an output that does not flip clears at barrierAt+period,
// driven by the pace timerfd. On a one-second output (so nothing expires during
// the test) the timerfd must be armed, no later than that deadline (other waits,
// like held buffers, may be earlier); and its callback, run once the deadline
// has passed, applies the queued commit.
func TestPaceTimerfdArmedForBarrierDeadline(t *testing.T) {
	s, events, commands, _, dir := contentServerWith(t, Options{Outputs: slowOutputs}, testLog())
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	placeWindow(t, s, commands, w, "HEADLESS-1")
	fifoQueue(t, c, surf)
	var remaining, want, period time.Duration
	var queued, left int
	var barrier bool
	var err error
	s.display.Do(func() {
		root := s.windows[w.ID].xdg.surface
		period, queued = s.outputPeriod(s.fifoOutput(root)), len(root.queue)
		var spec unix.ItimerSpec
		err = unix.TimerfdGettime(s.paceFD, &spec)
		remaining = time.Duration(spec.Value.Nano())
		want = time.Until(root.barrierAt.Add(period))
		// The deadline passes: the timerfd callback clears the barrier.
		root.barrierAt = time.Now().Add(-period)
		s.onPaceTimer()
		barrier, left = root.barrier, len(root.queue)
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued != 1 || period != time.Second {
		t.Fatalf("queued %d commits, period %v: the second commit must wait for the barrier", queued, period)
	}
	if remaining <= 0 || remaining > want+50*time.Millisecond {
		t.Fatalf("pace timerfd fires in %v, want armed at or before barrierAt+period (%v)", remaining, want)
	}
	if barrier || left != 0 {
		t.Fatalf("timer callback after the deadline: barrier %v, %d queued", barrier, left)
	}
}

func testLog() zerowrap.Logger {
	return logging.For(context.Background(), "wayland")
}

// fifoQueue sets a barrier with one commit and queues a second commit behind
// wait_barrier.
func fifoQueue(t *testing.T, c *wlturbo.Display, surf uint32) {
	t.Helper()
	manager := bindProtocol(t, c, "wp_fifo_manager_v1")
	f := c.AllocateID()
	registerProtocol(t, c, f)
	requestProtocol(t, c, manager, fifo.WpFifoManagerV1RequestGetFifo, f, surf)
	buf := shmBuffer(t, c)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}
