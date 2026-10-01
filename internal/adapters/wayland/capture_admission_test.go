package wayland

import (
	"context"
	"testing"
	"time"

	ext "github.com/bnema/go-wayland-bindings/server/extimagecopycapture"
	wlr "github.com/bnema/go-wayland-bindings/server/wlrscreencopy"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// admissionHarness runs a real Server with real protocol clients. All fields
// except the server internals read through display.Do are test-goroutine only.
type admissionHarness struct {
	s        *Server
	dir      string
	requests chan ports.CaptureRequest
	replies  chan ports.CaptureDone
	held     []ports.CaptureRequest // dup'd descriptors handed to the test
}

type admissionClient struct {
	c       *wlturbo.Display
	wlr     uint32
	output  uint32
	buf     uint32
	session uint32 // ext session, created on first use
	extMgr  uint32
}

// pendingCapture is one capture request and the events of its frame.
type pendingCapture struct {
	frame  uint32
	events *captureEvents
	failed uint16 // opcode of the failed event
	ext    bool
}

func newAdmissionHarness(t *testing.T, queue int) *admissionHarness {
	t.Helper()
	return newAdmissionHarnessWith(t, queue, Options{CaptureAllow: allowStore(t, "*\n")}, logging.For(context.Background(), "wayland"))
}

// newAdmissionHarnessWith runs the server with extra options (RuntimeDir and
// Outputs are the harness's) and a logger.
func newAdmissionHarnessWith(t *testing.T, queue int, opts Options, log zerowrap.Logger) *admissionHarness {
	t.Helper()
	h := &admissionHarness{dir: t.TempDir(), requests: make(chan ports.CaptureRequest, queue), replies: make(chan ports.CaptureDone, maxCaptureInflight+2)}
	// Registered first so it runs after Run stopped and the clients closed:
	// close every dup'd descriptor, including the ones a failed test left in
	// the queue or in held.
	t.Cleanup(func() {
		for len(h.requests) > 0 {
			h.held = append(h.held, <-h.requests)
		}
		for _, r := range h.held {
			_ = r.Dst.File.Close()
		}
	})
	out := ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 2, Height: 2}, Width: 2, Height: 2, Scale: 1}}
	opts.RuntimeDir, opts.Outputs = h.dir, out
	s, err := New(opts, Channels{Captures: h.requests, Captured: h.replies}, log)
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return h
}

func (h *admissionHarness) client(t *testing.T) *admissionClient {
	t.Helper()
	return h.clientOn(t, protocolClient(t, h.s, h.dir))
}

// clientOn binds the capture globals on an already connected client.
func (h *admissionHarness) clientOn(t *testing.T, c *wlturbo.Display) *admissionClient {
	t.Helper()
	g, ok := c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("output missing")
	}
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	_, buf, fd := captureSmallBuffer(t, c)
	t.Cleanup(func() { _ = unix.Close(fd) })
	return &admissionClient{c: c, wlr: bindVersion(t, c, "zwlr_screencopy_manager_v1", 3), output: out, buf: buf}
}

func (a *admissionClient) newFrame() (uint32, *captureEvents) {
	frame := a.c.AllocateID()
	ev := &captureEvents{events: make(chan uint16, 8)}
	ev.SetID(frame)
	registerWireProxy(a.c, ev)
	return frame, ev
}

// captureWlr asks for a wlr capture of the whole output.
func (a *admissionClient) captureWlr(t *testing.T) pendingCapture {
	t.Helper()
	frame, ev := a.newFrame()
	requestProtocol(t, a.c, a.wlr, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), a.output)
	requestProtocol(t, a.c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, a.buf)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return pendingCapture{frame: frame, events: ev, failed: uint16(wlr.ZwlrScreencopyFrameV1EventFailed)}
}

// captureExt asks for an ext capture. The session allows one live frame, so
// the caller destroys the previous frame first.
func (a *admissionClient) captureExt(t *testing.T) pendingCapture {
	t.Helper()
	if a.session == 0 {
		a.extMgr, a.session, _ = captureTestExt(t, a.c, a.output)
	}
	frame, ev := a.newFrame()
	requestProtocol(t, a.c, a.session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame)
	requestProtocol(t, a.c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, a.buf)
	requestProtocol(t, a.c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return pendingCapture{frame: frame, events: ev, failed: uint16(ext.ExtImageCopyCaptureFrameV1EventFailed), ext: true}
}

func (a *admissionClient) destroy(t *testing.T, p pendingCapture) {
	t.Helper()
	op := wlr.ZwlrScreencopyFrameV1RequestDestroy
	if p.ext {
		op = ext.ExtImageCopyCaptureFrameV1RequestDestroy
	}
	requestProtocol(t, a.c, p.frame, op)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

func (h *admissionHarness) inflight(t *testing.T) int {
	t.Helper()
	n := -1
	if !h.s.display.Do(func() { n = len(h.s.captureInflight) }) {
		t.Fatal("display stopped")
	}
	return n
}

func (h *admissionHarness) waitInflight(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.inflight(t) != want {
		if time.Now().After(deadline) {
			t.Fatalf("inflight %d, want %d", h.inflight(t), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// accepted reports whether the capture reached the backend queue; a refused
// capture is answered with failed and queues no request. Accepted requests
// are kept in held so cleanup closes their descriptors.
func (h *admissionHarness) accepted(t *testing.T, p pendingCapture) (ports.CaptureRequest, bool) {
	t.Helper()
	var last uint16
	for len(p.events.events) > 0 {
		last = <-p.events.events
	}
	select {
	case req := <-h.requests:
		if last == p.failed {
			t.Fatal("failed frame queued a request")
		}
		h.held = append(h.held, req)
		return req, true
	default:
		if last != p.failed {
			t.Fatalf("refused capture not failed: last event %d", last)
		}
		return ports.CaptureRequest{}, false
	}
}

func (h *admissionHarness) complete(t *testing.T, req ports.CaptureRequest, remaining int) {
	t.Helper()
	_ = req.Dst.File.Close()
	h.replies <- ports.CaptureDone{ID: req.ID, Output: req.Output, Time: time.Now()}
	h.waitInflight(t, remaining)
}

// saturate fills the cap with captures whose frames are destroyed right away,
// and returns their requests.
func (h *admissionHarness) saturate(t *testing.T, a *admissionClient, capture func(*testing.T) pendingCapture) []ports.CaptureRequest {
	t.Helper()
	var reqs []ports.CaptureRequest
	for i := 0; i < maxCaptureInflight; i++ {
		p := capture(t)
		req, ok := h.accepted(t, p)
		if !ok {
			t.Fatalf("capture %d refused below the cap", i)
		}
		reqs = append(reqs, req)
		a.destroy(t, p)
	}
	if got := h.inflight(t); got != maxCaptureInflight {
		t.Fatalf("inflight %d after destroying frames", got)
	}
	return reqs
}

func TestCaptureAdmissionSaturation(t *testing.T) {
	h := newAdmissionHarness(t, maxCaptureInflight+2)
	a := h.client(t)
	var frames []pendingCapture
	var reqs []ports.CaptureRequest
	for i := 0; i < maxCaptureInflight; i++ {
		p := a.captureWlr(t)
		req, ok := h.accepted(t, p)
		if !ok {
			t.Fatalf("capture %d refused below the cap", i)
		}
		reqs, frames = append(reqs, req), append(frames, p)
	}
	if got := h.inflight(t); got != maxCaptureInflight {
		t.Fatalf("inflight %d", got)
	}
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("capture admitted over the cap")
	}
	// Destroying the client frames must not release the backend's credit.
	for _, p := range frames {
		a.destroy(t, p)
	}
	if got := h.inflight(t); got != maxCaptureInflight {
		t.Fatalf("inflight %d after destroy", got)
	}
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("destroying frames bypassed the cap")
	}
	// A backend completion for a destroyed frame restores exactly one credit.
	h.complete(t, reqs[0], maxCaptureInflight-1)
	req, ok := h.accepted(t, a.captureWlr(t))
	if !ok {
		t.Fatal("completion did not restore credit")
	}
	h.waitInflight(t, maxCaptureInflight)
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("cap exceeded after refill")
	}
	h.complete(t, req, maxCaptureInflight-1)
	for i, r := range reqs[1:] {
		h.complete(t, r, maxCaptureInflight-2-i)
	}
	h.waitInflight(t, 0)
}

func TestExtCaptureAdmissionSaturation(t *testing.T) {
	h := newAdmissionHarness(t, maxCaptureInflight+2)
	a := h.client(t)
	reqs := h.saturate(t, a, a.captureExt)
	// Every frame is destroyed, yet the cap still holds.
	p := a.captureExt(t)
	if _, ok := h.accepted(t, p); ok {
		t.Fatal("destroyed ext frames bypassed the cap")
	}
	a.destroy(t, p)
	h.complete(t, reqs[0], maxCaptureInflight-1)
	p = a.captureExt(t)
	req, ok := h.accepted(t, p)
	if !ok {
		t.Fatal("completion did not restore ext credit")
	}
	a.destroy(t, p)
	h.waitInflight(t, maxCaptureInflight)
	if _, ok := h.accepted(t, a.captureExt(t)); ok {
		t.Fatal("cap exceeded after ext refill")
	}
	h.complete(t, req, maxCaptureInflight-1)
	for i, r := range reqs[1:] {
		h.complete(t, r, maxCaptureInflight-2-i)
	}
	h.waitInflight(t, 0)
}

func TestCaptureAdmissionSurvivesDisconnect(t *testing.T) {
	h := newAdmissionHarness(t, maxCaptureInflight+2)
	a := h.client(t)
	reqs := h.saturate(t, a, a.captureExt)
	if err := a.c.Close(); err != nil {
		t.Fatal(err)
	}
	// Wait until the display loop dropped the dead client's reply callbacks.
	deadline := time.Now().Add(2 * time.Second)
	for {
		replies := -1
		if !h.s.display.Do(func() { replies = len(h.s.captureReplies) }) {
			t.Fatal("display stopped")
		}
		if replies == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d reply callbacks retained after disconnect", replies)
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.inflight(t); got != maxCaptureInflight {
		t.Fatalf("inflight %d after disconnect", got)
	}
	// A new client cannot use the dead client's credits until completion.
	b := h.client(t)
	if _, ok := h.accepted(t, b.captureWlr(t)); ok {
		t.Fatal("disconnect bypassed the cap")
	}
	h.complete(t, reqs[0], maxCaptureInflight-1)
	req, ok := h.accepted(t, b.captureWlr(t))
	if !ok {
		t.Fatal("completion after disconnect did not restore credit")
	}
	h.complete(t, req, maxCaptureInflight-1)
	for i, r := range reqs[1:] {
		h.complete(t, r, maxCaptureInflight-2-i)
	}
	h.waitInflight(t, 0)
}

func TestCaptureAdmissionReleasedOnEnqueueFailure(t *testing.T) {
	h := newAdmissionHarness(t, 1)
	a := h.client(t)
	// The first capture fills the one-slot queue and stays in flight.
	a.captureWlr(t)
	h.waitInflight(t, 1)
	// The second is refused by the full queue and must not keep its credit.
	p := a.captureWlr(t)
	h.waitInflight(t, 1)
	var last uint16
	for len(p.events.events) > 0 {
		last = <-p.events.events
	}
	if last != p.failed || len(h.requests) != 1 {
		t.Fatalf("last event %d, queued %d; want failed and 1", last, len(h.requests))
	}
	req := <-h.requests
	h.held = append(h.held, req)
	h.complete(t, req, 0)
	// With the queue drained and credit restored a new capture is accepted.
	req, ok := h.accepted(t, a.captureWlr(t))
	if !ok {
		t.Fatal("capture refused after enqueue failure")
	}
	h.complete(t, req, 0)
}
