package wayland

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

type captureEvents struct {
	wlturbo.BaseProxy
	events chan uint16
}

func (p *captureEvents) Dispatch(e *wlturbo.Event) { p.events <- e.Opcode }
func TestCaptureRegion(t *testing.T) {
	o := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Width: 300, Height: 200}, Scale: 1.5}}
	for _, tc := range []struct{ in, want image.Rectangle }{
		{image.Rect(0, 0, 100, 100), image.Rect(0, 0, 150, 150)},
		{image.Rect(-10, -10, 10, 10), image.Rect(0, 0, 15, 15)},
		{image.Rect(190, 120, 240, 160), image.Rect(285, 180, 300, 200)},
	} {
		if got := captureRegion(o, tc.in); got != tc.want {
			t.Errorf("%v: %v != %v", tc.in, got, tc.want)
		}
	}
}
func TestScreencopyProtocol(t *testing.T) {
	dir := t.TempDir()
	requests := make(chan ports.CaptureRequest, 1)
	replies := make(chan ports.CaptureDone, 1)
	s, err := New(Options{RuntimeDir: dir, Outputs: ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}}}, Channels{Captures: requests, Captured: replies}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	c := protocolClient(t, s, dir)
	var out uint32
	for name, g := range c.Registry().GetGlobals() {
		if g.Interface == "wl_output" {
			out = name
		}
	}
	if out == 0 {
		t.Fatal("output global missing")
	}
	outputID, err := c.Registry().BindID(out, "wl_output", 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, outputID)
	manager := bindVersion(t, c, "zwlr_screencopy_manager_v1", 3)
	frame := c.AllocateID()
	events := &captureEvents{events: make(chan uint16, 8)}
	events.SetID(frame)
	c.Context().Register(events)
	requestProtocol(t, c, manager, wlr.ZwlrScreencopyManagerV1RequestCaptureOutputRegion, frame, int32(0), outputID, int32(1), int32(1), int32(2), int32(2))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if op := <-events.events; op != uint16(wlr.ZwlrScreencopyFrameV1EventBuffer) {
		t.Fatalf("buffer: %d", op)
	}
	if op := <-events.events; op != uint16(wlr.ZwlrScreencopyFrameV1EventBufferDone) {
		t.Fatalf("done: %d", op)
	}
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("capture", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 16); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(2), int32(2), int32(8), uint32(wayland.ShmFormatXrgb8888))
	requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopyWithDamage, buf)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		if req.Region != image.Rect(1, 1, 3, 3) {
			t.Errorf("region: %v", req.Region)
		}
		if _, err := req.Dst.File.WriteAt([]byte{1, 2, 3, 255}, 0); err != nil {
			t.Fatal(err)
		}
		if err := req.Dst.File.Close(); err != nil {
			t.Fatal(err)
		}
		replies <- ports.CaptureDone{ID: req.ID, Output: req.Output, Time: time.Now()}
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint16{uint16(wlr.ZwlrScreencopyFrameV1EventFlags), uint16(wlr.ZwlrScreencopyFrameV1EventDamage), uint16(wlr.ZwlrScreencopyFrameV1EventReady)} {
		var got uint16
		select {
		case got = <-events.events:
		case <-time.After(2 * time.Second):
			t.Fatalf("missing event %d", want)
		}
		if got != want {
			t.Errorf("event %d, want %d", got, want)
		}
	}
	data := make([]byte, 4)
	if _, err := unix.Pread(fd, data, 0); err != nil {
		t.Fatal(err)
	}
	if data[0] != 1 {
		t.Fatal(data)
	}
}
