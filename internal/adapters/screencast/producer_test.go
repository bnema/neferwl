package screencast

import (
	"errors"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-pipewire/pipewire"
	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

// producer is a service whose owner state is driven directly by the test, on
// the test goroutine (the owner): a generated mock bus, PipeWire core and
// video source stand where the real ones would be.
type producer struct {
	t    *testing.T
	s    *Service
	st   *stream
	src  *mockvideoSource
	core *mockvideoCore
	bus  *mockbus
	now  time.Time
	buf  *pipewire.VideoBuffer
	cmds chan ports.CastCommand
}

func newProducer(t *testing.T) *producer {
	t.Helper()
	cmds := make(chan ports.CastCommand, 16)
	p := &producer{t: t, now: time.Unix(1000, 0), cmds: cmds}
	p.s = New(func() *captureallow.Policy { return captureallow.Disabled() }, cmds, testLog(&logBuf{}))
	t.Cleanup(func() { _ = p.s.poller.Close() })
	p.bus = newMockbus(t)
	p.bus.EXPECT().NewSignalTo(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(string, string, string, string, string) *zerobus.Encoder { return &zerobus.Encoder{} }).Maybe()
	p.bus.EXPECT().Send().Return(1, nil).Maybe()
	p.s.conn = p.bus
	p.src = newMockvideoSource(t)
	p.core = newMockvideoCore(t)
	p.s.pwc = p.core
	p.buf = &pipewire.VideoBuffer{Index: 3, Data: make([]byte, 64*32*4), Stride: 64 * 4, Width: 64, Height: 32, Format: pipewire.VideoFormatBGRx}

	sess := &p.s.sessions[0]
	*sess = session{used: true, id: 1, owner: ":1.7", path: prefixSession + "1", started: true}
	st := &sess.streams[0]
	*st = stream{used: true, sess: sess, id: 1, cast: 1, path: prefixStream + "1", output: "HEADLESS-1", maxFPS: 60, pwState: -2, needOpen: true}
	st.h2 = streamHandler{st: st}
	p.st = st
	return p
}

func (p *producer) cmd() ports.CastCommand {
	p.t.Helper()
	select {
	case c := <-p.cmds:
		return c
	default:
		p.t.Fatal("no command")
		return ports.CastCommand{}
	}
}

func (p *producer) none() {
	p.t.Helper()
	select {
	case c := <-p.cmds:
		p.t.Fatalf("unexpected command %+v", c)
	default:
	}
}

// streaming opens the cast, sizes it, creates the source and brings it to
// STREAMING with the negotiated format.
func (p *producer) streaming() {
	p.t.Helper()
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != ports.CastOpen || c.ID != 1 || c.Output != "HEADLESS-1" {
		p.t.Fatalf("open %+v", c)
	}
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastSize, ID: 1, Width: 64, Height: 32})
	p.core.EXPECT().NewVideoSource(mock.Anything, mock.Anything).RunAndReturn(
		func(cfg pipewire.VideoConfig, h pipewire.VideoHandler) (videoSource, error) {
			if cfg.Width != 64 || cfg.Height != 32 || cfg.MaxFPS != 60 {
				p.t.Errorf("config %+v", cfg)
			}
			return p.src, nil
		}).Once()
	p.s.pump(p.st, p.now)
	p.st.h2.StateChanged(pipewire.StreamStatePaused, 42, "")
	p.st.h2.FormatChanged(pipewire.VideoFormatBGRx, 64, 32, 256)
	p.st.h2.StateChanged(pipewire.StreamStateStreaming, 42, "")
}

// The node id goes to the session owner alone, once, as the stream's signal.
func TestAnnouncesNodeToOwnerOnce(t *testing.T) {
	p := newProducer(t)
	var dest, path, member string
	p.bus.ExpectedCalls = nil
	p.bus.EXPECT().NewSignalTo(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(d, pa, iface, m, sig string) *zerobus.Encoder {
			dest, path, member = d, pa, m
			if iface != ifaceStream || sig != "u" {
				t.Errorf("signal %s %s", iface, sig)
			}
			return &zerobus.Encoder{}
		}).Once()
	p.bus.EXPECT().Send().Return(1, nil).Once()
	p.streaming()
	p.src.EXPECT().Dequeue().Return(nil).Maybe()
	p.s.pump(p.st, p.now)
	p.s.pump(p.st, p.now)
	if dest != ":1.7" || path != prefixStream+"1" || member != "PipeWireStreamAdded" {
		t.Fatalf("signal to %q %q %q", dest, path, member)
	}
}

// Nothing is requested without damage, except the first frame.
func TestPacingForcesFirstFrameThenWaitsForDamage(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	c := p.cmd()
	if c.Kind != ports.CastFrame || c.Buf != 3 || c.Stride != 256 || c.Width != 64 || c.Height != 32 || c.Format != shmXRGB8888 || len(c.Data) != 64*32*4 || &c.Data[0] != &p.buf.Data[0] {
		t.Fatalf("frame %+v", c)
	}
	// Done: queued, then triggered, in that order.
	var order []string
	p.src.EXPECT().Queue(p.buf, uint64(1), mock.Anything, false).Run(func(*pipewire.VideoBuffer, uint64, time.Duration, bool) { order = append(order, "queue") }).Once()
	p.src.EXPECT().Trigger().Run(func() { order = append(order, "trigger") }).Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3, Time: time.Unix(5, 0)})
	if len(order) != 2 || order[0] != "queue" || order[1] != "trigger" {
		t.Fatalf("order %v", order)
	}
	// No damage: no request however much time passes (Dequeue is not expected).
	p.now = p.now.Add(time.Second)
	p.s.pump(p.st, p.now)
	p.none()
}

// At most one frame per interval, damage or not; the poll timeout is the
// time left.
func TestPacingOnePerInterval(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Twice()
	p.s.pump(p.st, p.now)
	p.cmd()
	p.src.EXPECT().Queue(mock.Anything, mock.Anything, mock.Anything, false).Return().Once()
	p.src.EXPECT().Trigger().Return().Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3})
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastDamage, ID: 1})
	p.now = p.now.Add(5 * time.Millisecond)
	p.s.pumpAll(p.now)
	p.none()
	if got := p.s.timeout(); got < 11 || got > 12 {
		t.Fatalf("timeout %d ms, want the ~11.7 ms left of 16.7", got)
	}
	p.now = p.now.Add(12 * time.Millisecond)
	p.s.pumpAll(p.now)
	if c := p.cmd(); c.Kind != ports.CastFrame {
		t.Fatalf("%+v", c)
	}
	if got := p.s.timeout(); got != -1 {
		t.Fatalf("idle timeout %d", got)
	}
}

// A new size waits for the frame in flight, then renegotiates.
func TestResizeWaitsForInflightFrame(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	p.cmd()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastSize, ID: 1, Width: 32, Height: 16})
	p.s.pump(p.st, p.now) // Resize is not expected while the frame is in flight
	p.src.EXPECT().Resize(32, 16).Return(nil).Once()
	p.src.EXPECT().Release(p.buf).Return().Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3, Err: ports.ErrCaptureTransient})
	p.s.pump(p.st, p.now)
	// No frame until the new format is negotiated.
	p.now = p.now.Add(time.Second)
	p.s.pump(p.st, p.now)
	p.none()
}

// A failed frame gives the buffer back; three in a row stop the stream.
func TestFrameErrorsReleaseAndStop(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf)
	p.src.EXPECT().Release(p.buf).Return()
	for i := range maxFrameErrors {
		p.s.pump(p.st, p.now)
		if c := p.cmd(); c.Kind != ports.CastFrame {
			t.Fatalf("frame %d: %+v", i, c)
		}
		p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3, Err: errTest})
		p.now = p.now.Add(time.Second)
	}
	if !p.st.closing || !p.s.sessions[0].dying {
		t.Fatal("stream not stopped after three errors")
	}
	p.src.EXPECT().Destroy().Return().Once()
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != ports.CastClose || c.ID != 1 {
		t.Fatalf("%+v", c)
	}
	if p.st.used {
		t.Fatal("stream slot not freed")
	}
}

// Ending the session with a frame in flight closes the cast at once but
// keeps the PipeWire node until the frame's CastFrameDone.
func TestTeardownWaitsForFrameDone(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	p.cmd()
	p.s.closeSession(p.st.sess, true)
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != ports.CastClose {
		t.Fatalf("%+v", c)
	}
	p.s.pump(p.st, p.now)
	p.s.reap()
	if !p.st.used || !p.s.sessions[0].used {
		t.Fatal("released while a frame is in flight") // Destroy is not expected yet
	}
	p.src.EXPECT().Release(p.buf).Return().Once()
	p.src.EXPECT().Destroy().Return().Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3})
	p.s.pump(p.st, p.now)
	p.s.reap()
	if p.st.used || p.s.sessions[0].used {
		t.Fatal("stream and session not freed after the frame")
	}
}

// A cast that wayland ended closes the session and signals Closed.
func TestCastStoppedClosesSession(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.bus.ExpectedCalls = nil
	var member string
	p.bus.EXPECT().NewSignalTo(":1.7", prefixSession+"1", ifaceSession, "Closed", "").RunAndReturn(
		func(_, _, _, m, _ string) *zerobus.Encoder {
			member = m
			return &zerobus.Encoder{}
		}).Once()
	p.bus.EXPECT().Send().Return(1, nil).Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastStopped, ID: 1, Reason: ports.CaptureReasonWindowGone})
	if member != "Closed" {
		t.Fatal("Closed not signalled")
	}
	p.src.EXPECT().Destroy().Return().Once()
	p.s.pump(p.st, p.now)
	// Wayland ended it itself: no CastClose.
	p.none()
}

// A PipeWire stream that went to ERROR ends the session.
func TestPipeWireErrorEndsSession(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.st.h2.StateChanged(pipewire.StreamStateError, 42, "gone")
	p.s.pump(p.st, p.now)
	if !p.s.sessions[0].dying {
		t.Fatal("session still open")
	}
}

var errTest = errors.New("frame failed")

// The steady damage -> frame -> done cycle allocates nothing. Measured with
// real values: the sink, the pacing and the settle logic. The PipeWire calls
// of the cycle (Dequeue, Queue, Trigger) are the VideoSource's own,
// measured in purego-pipewire (TestVideoSourceFrameAllocations); a mock in
// the closure would record every call and grow.
func TestCastProducerAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the wake callback allocate")
	}
	p := newProducer(t)
	p.cmds = make(chan ports.CastCommand, 4)
	p.s.cmds = p.cmds
	st := p.st
	st.pwState, st.w, st.h, st.fw, st.fh, st.haveSize = pipewire.StreamStateStreaming, 64, 32, 64, 32, true
	cmds := p.cmds
	b := p.buf
	damage := ports.CastEvent{Kind: ports.CastDamage, ID: 1}
	done := ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: b.Index, Time: time.Unix(5, 0)}
	now := p.now
	cycle := func() {
		p.s.Push(damage)
		evs, _, _, _ := p.s.take()
		for i := range evs {
			p.s.onCastEvent(&evs[i])
		}
		now = now.Add(time.Second)
		if !p.s.frameDue(st, now) || !p.s.issueFrame(st, b, now) {
			t.Fatal("no frame")
		}
		<-cmds
		p.s.Push(done)
		evs, _, _, _ = p.s.take()
		if p.s.settleFrame(st, &evs[0]) != b {
			t.Fatal("frame not settled")
		}
		st.buf = nil
	}
	for range 8 {
		cycle()
	}
	if n := testing.AllocsPerRun(200, cycle); n != 0 {
		t.Fatalf("%v allocations per frame, want 0", n)
	}
}

// The consumer leaves: the node goes to PAUSED and no frame is requested,
// whatever damage and time bring, while the session stays open. A frame
// already in flight is released, not queued. When the node streams again the
// first frame is forced.
func TestPausedNodeGetsNoFramesAndSessionStays(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Twice()
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != ports.CastFrame {
		t.Fatalf("%+v", c)
	}
	// Paused with the frame in flight: it is given back, not queued (no
	// Queue or Trigger expectation).
	p.st.h2.StateChanged(pipewire.StreamStatePaused, 42, "")
	p.src.EXPECT().Release(p.buf).Return().Once()
	p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3, Time: time.Unix(5, 0)})
	for range 5 {
		p.s.onCastEvent(&ports.CastEvent{Kind: ports.CastDamage, ID: 1})
		p.now = p.now.Add(time.Second)
		p.s.pumpAll(p.now) // Dequeue is not expected a second time while paused
		p.none()
	}
	if p.s.sessions[0].dying || p.st.closing {
		t.Fatal("the session ended with the consumer")
	}
	if got := p.s.timeout(); got != -1 {
		t.Fatalf("paused stream asks for a timeout of %d ms", got)
	}
	// A consumer is back: the picture goes out at once, damage or not.
	p.st.h2.StateChanged(pipewire.StreamStateStreaming, 42, "")
	p.s.pumpAll(p.now)
	if c := p.cmd(); c.Kind != ports.CastFrame {
		t.Fatalf("no frame after the node streams again: %+v", c)
	}
}

// Shutdown pumps every stream: CastClose goes out, the node is destroyed and
// PipeWire closed, with no frame in flight.
func TestShutdownClosesStreams(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	var order []string
	p.src.EXPECT().Destroy().Run(func() { order = append(order, "destroy") }).Once()
	p.core.EXPECT().Close().Run(func() { order = append(order, "close") }).Once()
	p.s.shutdown()
	if c := p.cmd(); c.Kind != ports.CastClose || c.ID != 1 {
		t.Fatalf("%+v", c)
	}
	if len(order) != 2 || order[0] != "destroy" || order[1] != "close" || p.s.pwc != nil {
		t.Fatalf("order %v", order)
	}
	if p.st.used || p.s.sessions[0].used {
		t.Fatal("slots not freed")
	}
}

// A stream opened on the wayland side that has no node yet is closed too.
func TestShutdownClosesStreamWithoutSource(t *testing.T) {
	p := newProducer(t)
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != ports.CastOpen {
		t.Fatalf("%+v", c)
	}
	p.core.EXPECT().Close().Return().Once()
	p.s.shutdown()
	if c := p.cmd(); c.Kind != ports.CastClose || c.ID != 1 {
		t.Fatalf("%+v", c)
	}
	if p.st.used {
		t.Fatal("stream not freed")
	}
}

// A frame in flight at shutdown is waited for: nothing is destroyed before
// its CastFrameDone, then the buffer is released and the node destroyed.
func TestShutdownWaitsForFrameInFlight(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	p.cmd()
	var order []string
	p.src.EXPECT().Release(p.buf).Run(func(*pipewire.VideoBuffer) { order = append(order, "release") }).Once()
	p.src.EXPECT().Destroy().Run(func() { order = append(order, "destroy") }).Once()
	p.core.EXPECT().Close().Run(func() { order = append(order, "close") }).Once()
	go func() {
		time.Sleep(30 * time.Millisecond)
		p.s.Push(ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 3})
	}()
	start := time.Now()
	p.s.shutdown()
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("shutdown did not wait for the frame")
	}
	if c := p.cmd(); c.Kind != ports.CastClose {
		t.Fatalf("%+v", c)
	}
	if len(order) != 3 || order[0] != "release" || order[1] != "destroy" || order[2] != "close" {
		t.Fatalf("order %v", order)
	}
}

// When the frame never comes back, nothing is destroyed or closed (the
// source's handler and its buffer are still in use), the storage the
// handler points into is left untouched and abandoned, and the service has
// a fresh table: repeated reconnects do not exhaust it.
func TestShutdownTimeoutKeepsHandlerStateAndFreesTable(t *testing.T) {
	p := newProducer(t)
	p.s.drain = 20 * time.Millisecond
	p.streaming()
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	p.cmd()
	old := p.s.sessions
	p.s.shutdown() // no Destroy, Release or Close is expected
	if c := p.cmd(); c.Kind != ports.CastClose {
		t.Fatalf("%+v", c)
	}
	if p.s.sessions == old {
		t.Fatal("the table the leaked handler points into is still in use")
	}
	if !p.st.used || p.st.src == nil || !p.st.inflight || p.st.h2.st != p.st {
		t.Fatal("the leaked stream's state was reset")
	}
	if p.s.streamsLive() || p.s.pwc != nil {
		t.Fatal("new table not empty or PipeWire kept")
	}
	// The handler of the leaked source still works on its own storage.
	p.st.h2.StateChanged(pipewire.StreamStatePaused, 42, "")
	if p.st.pwState != pipewire.StreamStatePaused {
		t.Fatal("handler lost its stream")
	}
	// A new attempt starts clean and can fill its table again.
	p.s.reset(p.s.ctx, p.bus)
	for i := range maxSessions {
		p.s.sessions[i] = session{used: true, id: uint32(i + 1)}
	}
}
