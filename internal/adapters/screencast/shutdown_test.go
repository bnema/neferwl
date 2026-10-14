package screencast

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-pipewire/pipewire"
	"github.com/stretchr/testify/mock"
)

// liveCast is a cast on a private bus, brought to STREAMING with a mocked
// PipeWire source, so a test can lose the bus under it.
type liveCast struct {
	h   *svcHarness
	src *mockvideoSource
	buf *pipewire.VideoBuffer
	// onLoop runs functions inside the PipeWire loop iteration, where the
	// real handler callbacks run.
	onLoop chan func()
	hdl    chan pipewire.VideoHandler
}

func (l *liveCast) loop(f func()) {
	l.onLoop <- f
	l.h.svc.Push(ports.CastEvent{Kind: ports.CastDamage, ID: 1 << 40}) // wakes the owner
}

func startLiveCast(t *testing.T, withFrame bool, drain time.Duration) *liveCast {
	t.Helper()
	l := &liveCast{onLoop: make(chan func(), 8), hdl: make(chan pipewire.VideoHandler, 1)}
	l.h, _ = ready(t, harnessOpts{drain: drain, iterate: func() {
		select {
		case f := <-l.onLoop:
			f()
		default:
		}
	}})
	h := l.h
	c := h.client()
	l.src = newMockvideoSource(t)
	l.buf = &pipewire.VideoBuffer{Index: 2, Data: make([]byte, 64*32*4), Stride: 256, Width: 64, Height: 32, Format: pipewire.VideoFormatBGRx}
	h.core.EXPECT().NewVideoSource(mock.Anything, mock.Anything).RunAndReturn(
		func(_ pipewire.VideoConfig, hd pipewire.VideoHandler) (videoSource, error) {
			l.hdl <- hd
			return l.src, nil
		}).Once()
	if withFrame {
		l.src.EXPECT().Dequeue().Return(l.buf).Once()
	} else {
		l.src.EXPECT().Dequeue().Return(nil).Maybe()
	}

	sess := createSession(t, c)
	if name, _ := recordMonitor(c, sess, "HEADLESS-1"); name != "" {
		t.Fatal(name)
	}
	if name, _ := callErr(c, nameScreenCast, sess, ifaceSession, "Start", "", nil); name != "" {
		t.Fatal(name)
	}
	if cmd := recvCmd(t, h); cmd.Kind != ports.CastOpen {
		t.Fatalf("%+v", cmd)
	}
	h.svc.Push(ports.CastEvent{Kind: ports.CastSize, ID: 1, Width: 64, Height: 32})
	var hd pipewire.VideoHandler
	select {
	case hd = <-l.hdl:
	case <-time.After(5 * time.Second):
		t.Fatal("no PipeWire source")
	}
	l.loop(func() {
		hd.StateChanged(pipewire.StreamStatePaused, 42, "")
		hd.FormatChanged(pipewire.VideoFormatBGRx, 64, 32, 256)
		hd.StateChanged(pipewire.StreamStateStreaming, 42, "")
	})
	if withFrame {
		if cmd := recvCmd(t, h); cmd.Kind != ports.CastFrame || cmd.Buf != 2 {
			t.Fatalf("%+v", cmd)
		}
	}
	return l
}

func recvCmd(t *testing.T, h *svcHarness) ports.CastCommand {
	t.Helper()
	select {
	case c := <-h.cmds:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no command\n" + h.log.String())
		return ports.CastCommand{}
	}
}

func waitClosed(t *testing.T, h *svcHarness) {
	t.Helper()
	select {
	case <-h.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("PipeWire not closed\n" + h.log.String())
	}
}

// Losing the bus with a live stream and no frame in flight: the cast is
// closed on the wayland side, the source destroyed, PipeWire closed.
func TestBusLossTearsDownStream(t *testing.T) {
	l := startLiveCast(t, false, 0)
	l.src.EXPECT().Destroy().Return().Once()
	l.h.killBus()
	if cmd := recvCmd(t, l.h); cmd.Kind != ports.CastClose || cmd.ID != 1 {
		t.Fatalf("%+v", cmd)
	}
	waitClosed(t, l.h)
}

// With a frame in flight: CastClose goes out at once, but the source is
// destroyed only after the frame's CastFrameDone, even when that comes later
// than drain: the service runs on, so nothing is left behind. The order is
// synchronized by channels: the buffer must be released before the node is
// destroyed, and PipeWire closed after both.
func TestBusLossWaitsForFrameInFlight(t *testing.T) {
	l := startLiveCast(t, true, time.Nanosecond)
	l.h.killBus()
	if cmd := recvCmd(t, l.h); cmd.Kind != ports.CastClose || cmd.ID != 1 {
		t.Fatalf("%+v", cmd)
	}
	var delivered atomic.Bool
	steps := make(chan string, 3)
	step := func(name string) {
		if !delivered.Load() {
			t.Errorf("%s before the CastFrameDone", name)
		}
		steps <- name
	}
	l.src.EXPECT().Release(l.buf).Run(func(*pipewire.VideoBuffer) { step("release") }).Once()
	l.src.EXPECT().Destroy().Run(func() { step("destroy") }).Once()
	delivered.Store(true)
	l.h.svc.Push(ports.CastEvent{Kind: ports.CastFrameDone, ID: 1, Buf: 2})
	waitClosed(t, l.h)
	if len(steps) != 2 || <-steps != "release" || <-steps != "destroy" {
		t.Fatal("release and destroy did not run in order before PipeWire closed")
	}
}

// The service itself stopping with a frame that is never answered returns
// within the bounded wait, and leaves the source and PipeWire alone.
func TestStopWithUnansweredFrameIsBounded(t *testing.T) {
	l := startLiveCast(t, true, 20*time.Millisecond)
	l.h.cancel()
	if cmd := recvCmd(t, l.h); cmd.Kind != ports.CastClose || cmd.ID != 1 {
		t.Fatalf("%+v", cmd)
	}
	select {
	case <-l.h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-l.h.closed:
		t.Fatal("PipeWire closed under a source with a frame in flight")
	default:
	}
}
