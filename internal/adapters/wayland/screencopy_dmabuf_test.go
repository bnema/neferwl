package wayland

import (
	"context"
	"image"
	"testing"
	"time"

	linuxdmabuf "github.com/bnema/go-wayland-bindings/server/linuxdmabuf"
	wlr "github.com/bnema/go-wayland-bindings/server/wlrscreencopy"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
)

var linearXRGB = domain.DMABufFormat{Format: fourccXRGB}

// screencopyDMABufServer serves an output of 4×4 with linux-dmabuf and, when
// capture is set, captures into linear XRGB8888 buffers.
//
//testpolicy:realtime Server.Run waits on its libwayland epoll fd; real Wayland socket with a wlturbo client
func screencopyDMABufServer(t *testing.T, capture []domain.DMABufFormat) (*Server, string, chan domain.CaptureRequest, chan domain.CaptureDone) {
	t.Helper()
	dir := t.TempDir()
	requests := make(chan domain.CaptureRequest, 1)
	replies := make(chan domain.CaptureDone, 1)
	sup := domain.DMABufSupport{Formats: []domain.DMABufFormat{linearXRGB, linearARGB}}
	s, err := New(Options{RuntimeDir: dir, CaptureAllow: allowStore(t, "*\n"), DMABuf: sup, CaptureDMABuf: capture, Outputs: domain.Layout{{Info: domain.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}}}, Channels{Captures: requests, Captured: replies}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-waylandDeadline(2 * time.Second):
			t.Error("Run hung")
		}
	})
	return s, dir, requests, replies
}

// nextFrameEvent returns the next opcode of a capture frame.
//
//testpolicy:realtime events arrive over a real Wayland socket
func nextFrameEvent(t *testing.T, events *captureEvents) uint16 {
	t.Helper()
	select {
	case op := <-events.events:
		return op
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("no event")
		return 0
	}
}

// v3 frames offer an XRGB8888 linux-dmabuf buffer between buffer and
// buffer_done; a matching dmabuf wl_buffer becomes a DMABuf destination with
// its own plane descriptors and is answered ready.
//
//testpolicy:realtime real Wayland unix socket between the display loop and a wlturbo client
func TestScreencopyDMABuf(t *testing.T) {
	s, dir, requests, replies := screencopyDMABufServer(t, []domain.DMABufFormat{linearXRGB})
	c := protocolClient(t, s, dir)
	g, ok := c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("wl_output missing")
	}
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	manager := bindVersion(t, c, "zwlr_screencopy_manager_v1", 3)
	frame := c.AllocateID()
	events := &captureEvents{events: make(chan uint16, 8)}
	events.SetID(frame)
	registerWireProxy(c, events)
	requestProtocol(t, c, manager, wlr.ZwlrScreencopyManagerV1RequestCaptureOutputRegion, frame, int32(0), out, int32(1), int32(1), int32(2), int32(2))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{wlr.ZwlrScreencopyFrameV1EventBuffer, wlr.ZwlrScreencopyFrameV1EventLinuxDmabuf, wlr.ZwlrScreencopyFrameV1EventBufferDone} {
		if op := nextFrameEvent(t, events); op != uint16(want) {
			t.Fatalf("event %d, want %d", op, want)
		}
	}
	// A linear 2×2 XRGB8888 dmabuf (memfd: the server checks bounds only).
	dg, _ := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	dm, err := bindWireID(c, dg.Name, dg.Interface, dmabufVersion)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.MemfdCreate("capture-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Ftruncate(fd, 4096); err != nil {
		t.Fatal(err)
	}
	p, buf := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
	if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreateImmed, buf, int32(2), int32(2), fourccXRGB, uint32(0))
	registerProtocol(t, c, buf)
	requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var req domain.CaptureRequest
	select {
	case req = <-requests:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("no request")
	}
	if !req.OneDestination() || req.DMABuf == nil || req.Region != image.Rect(1, 1, 3, 3) || req.DMABuf.Format != fourccXRGB || len(req.DMABuf.Planes) != 1 || req.DMABuf.Planes[0].Stride != 256 {
		t.Fatalf("request %+v", req)
	}
	var st, own unix.Stat_t
	if err := unix.Fstat(int(req.DMABuf.Planes[0].File.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(fd, &own); err != nil || st.Ino != own.Ino {
		t.Fatalf("plane is not the client's buffer: %v", err)
	}
	for _, pl := range req.DMABuf.Planes {
		_ = pl.File.Close()
	}
	deliverCapture(t, s, replies, domain.CaptureDone{ID: req.ID, Output: req.Output, Time: time.Now()})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{wlr.ZwlrScreencopyFrameV1EventFlags, wlr.ZwlrScreencopyFrameV1EventReady} {
		if op := nextFrameEvent(t, events); op != uint16(want) {
			t.Fatalf("event %d, want %d", op, want)
		}
	}
}

// Without capture formats nothing is offered, and a dmabuf wl_buffer is an
// invalid buffer.
func TestScreencopyNoDMABufOffer(t *testing.T) {
	s, dir, _, _ := screencopyDMABufServer(t, nil)
	c := protocolClient(t, s, dir)
	g, _ := c.Registry().FindGlobal("wl_output")
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	manager := bindVersion(t, c, "zwlr_screencopy_manager_v1", 3)
	frame := c.AllocateID()
	events := &captureEvents{events: make(chan uint16, 8)}
	events.SetID(frame)
	registerWireProxy(c, events)
	requestProtocol(t, c, manager, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), out)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{wlr.ZwlrScreencopyFrameV1EventBuffer, wlr.ZwlrScreencopyFrameV1EventBufferDone} {
		if op := nextFrameEvent(t, events); op != uint16(want) {
			t.Fatalf("event %d, want %d", op, want)
		}
	}
	dg, _ := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	dm, err := bindWireID(c, dg.Name, dg.Interface, dmabufVersion)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.MemfdCreate("capture-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Ftruncate(fd, 4096); err != nil {
		t.Fatal(err)
	}
	p, buf := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
	if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreateImmed, buf, int32(4), int32(4), fourccXRGB, uint32(0))
	registerProtocol(t, c, buf)
	requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
	expectProtocolError(t, c, frame, uint32(wlr.ZwlrScreencopyFrameV1ErrorInvalidBuffer))
}
