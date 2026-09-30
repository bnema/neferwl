package wayland

import (
	"context"
	"fmt"
	"image"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	source "github.com/bnema/purego-libwayland/protocol/extimagecapturesource"
	ext "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"

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
	s, err := New(Options{RuntimeDir: dir, CaptureAllow: allowStore(t, "*\n"), Outputs: ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}}}, Channels{Captures: requests, Captured: replies}, logging.For(context.Background(), "wayland"))
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
	outputID, err := bindWireID(c, out, "wl_output", 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, outputID)
	manager := bindVersion(t, c, "zwlr_screencopy_manager_v1", 3)
	frame := c.AllocateID()
	events := &captureEvents{events: make(chan uint16, 8)}
	events.SetID(frame)
	registerWireProxy(c, events)
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
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16)); err != nil {
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
		deliverCapture(t, s, replies, ports.CaptureDone{ID: req.ID, Output: req.Output, Time: time.Now()})
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

func captureTestClient(t *testing.T) (*Server, *wlturbo.Display, uint32) {
	t.Helper()
	s, _, _, dir := lifecycleServer(t)
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
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return s, c, out
}

func captureTestExt(t *testing.T, c *wlturbo.Display, out uint32) (uint32, uint32, uint32) {
	t.Helper()
	sourceManager := bindProtocol(t, c, "ext_output_image_capture_source_manager_v1")
	manager := bindProtocol(t, c, "ext_image_copy_capture_manager_v1")
	src, session := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, src)
	requestProtocol(t, c, sourceManager, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, out)
	events := &captureEvents{events: make(chan uint16, 16)}
	events.SetID(session)
	registerWireProxy(c, events)
	requestProtocol(t, c, manager, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, session, src, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint16{0, 1, 1, 4} {
		if got := <-events.events; got != want {
			t.Fatalf("session event %d != %d", got, want)
		}
	}
	return manager, session, src
}

func TestCaptureProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		object string
		code   uint32
	}{
		{"wlr already used", "wlr", uint32(wlr.ZwlrScreencopyFrameV1ErrorAlreadyUsed)},
		{"wlr invalid size", "wlr size", uint32(wlr.ZwlrScreencopyFrameV1ErrorInvalidBuffer)},
		{"wlr invalid format", "wlr format", uint32(wlr.ZwlrScreencopyFrameV1ErrorInvalidBuffer)},
		{"ext no buffer", "frame", uint32(ext.ExtImageCopyCaptureFrameV1ErrorNoBuffer)},
		{"ext already captured", "frame twice", uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured)},
		{"ext damage", "damage", uint32(ext.ExtImageCopyCaptureFrameV1ErrorInvalidBufferDamage)},
		{"ext duplicate frame", "session", uint32(ext.ExtImageCopyCaptureSessionV1ErrorDuplicateFrame)},
		{"ext invalid option", "manager", uint32(ext.ExtImageCopyCaptureManagerV1ErrorInvalidOption)},
		{"ext duplicate cursor session", "cursor", uint32(ext.ExtImageCopyCaptureCursorSessionV1ErrorDuplicateSession)},
		{"ext buffer constraints", "constraints", 0},
		{"ext attach after capture", "attach after", uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured)},
		{"ext damage after capture", "damage after", uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, out := captureTestClient(t)
			if strings.HasPrefix(tc.object, "wlr") {
				manager := bindVersion(t, c, "zwlr_screencopy_manager_v1", 3)
				frame := c.AllocateID()
				registerProtocol(t, c, frame)
				requestProtocol(t, c, manager, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), out)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				shm := bindProtocol(t, c, "wl_shm")
				fd, err := unix.MemfdCreate("bad-capture", 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				if err := unix.Ftruncate(fd, int64(testOutputs[0].Info.Width*testOutputs[0].Info.Height*4)); err != nil {
					t.Fatal(err)
				}
				pool, buf := c.AllocateID(), c.AllocateID()
				registerProtocol(t, c, pool)
				registerProtocol(t, c, buf)
				registerProtocol(t, c, shm)
				if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(testOutputs[0].Info.Width*testOutputs[0].Info.Height*4)); err != nil {
					t.Fatal(err)
				}
				w := int32(1)
				format := uint32(wayland.ShmFormatXrgb8888)
				if tc.object == "wlr format" {
					format = uint32(wayland.ShmFormatArgb8888)
				}
				if tc.object != "wlr size" {
					w = int32(testOutputs[0].Info.Width)
				}
				requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), w, int32(testOutputs[0].Info.Height), w*4, format)
				if tc.object == "wlr" {
					if err := c.Roundtrip(); err != nil {
						t.Fatal(err)
					}
					requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
					if err := c.Roundtrip(); err != nil {
						t.Fatal(err)
					}
				}
				requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
				expectProtocolError(t, c, frame, tc.code)
				return
			}
			manager, session, src := captureTestExt(t, c, out)
			switch tc.object {
			case "manager":
				requestProtocol(t, c, manager, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, c.AllocateID(), src, uint32(2))
				expectProtocolError(t, c, manager, tc.code)
				return
			case "cursor":
				seat := bindProtocol(t, c, "wl_seat")
				pointer := c.AllocateID()
				registerProtocol(t, c, pointer)
				requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
				registerProtocol(t, c, seat)
				cursor := c.AllocateID()
				registerProtocol(t, c, cursor)
				requestProtocol(t, c, manager, ext.ExtImageCopyCaptureManagerV1RequestCreatePointerCursorSession, cursor, src, pointer)
				registerProtocol(t, c, manager)
				sessionID := c.AllocateID()
				registerProtocol(t, c, sessionID)
				requestProtocol(t, c, cursor, ext.ExtImageCopyCaptureCursorSessionV1RequestGetCaptureSession, sessionID)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				requestProtocol(t, c, cursor, ext.ExtImageCopyCaptureCursorSessionV1RequestGetCaptureSession, c.AllocateID())
				expectProtocolError(t, c, cursor, tc.code)
				return
			}
			frame := c.AllocateID()
			frameEvents := &captureDetails{events: make(chan []uint32, 4)}
			frameEvents.SetID(frame)
			registerWireProxy(c, frameEvents)
			requestProtocol(t, c, session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame)
			switch tc.object {
			case "frame":
				requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
			case "constraints", "attach after", "damage after", "frame twice":
				shm := bindProtocol(t, c, "wl_shm")
				registerProtocol(t, c, shm)
				fd, err := unix.MemfdCreate("ext-capture", 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				if err := unix.Ftruncate(fd, 4); err != nil {
					t.Fatal(err)
				}
				pool, buf := c.AllocateID(), c.AllocateID()
				registerProtocol(t, c, pool)
				registerProtocol(t, c, buf)
				if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
					t.Fatal(err)
				}
				requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1), int32(1), int32(4), uint32(wayland.ShmFormatXrgb8888))
				requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, buf)
				requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
				if tc.object == "constraints" {
					if err := c.Roundtrip(); err != nil {
						t.Fatal(err)
					}
					if got := <-frameEvents.events; !reflect.DeepEqual(got, []uint32{uint32(ext.ExtImageCopyCaptureFrameV1EventFailed), uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints)}) {
						t.Fatalf("constraints: event %v", got)
					}
					return
				}
				switch tc.object {
				case "frame twice":
					requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
				case "attach after":
					requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, buf)
				case "damage after":
					requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestDamageBuffer, int32(0), int32(0), int32(1), int32(1))
				}
			case "damage":
				requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestDamageBuffer, int32(-1), int32(0), int32(1), int32(1))
			case "session":
				requestProtocol(t, c, session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, c.AllocateID())
			}
			object := frame
			if tc.object == "session" {
				object = session
			}
			expectProtocolError(t, c, object, tc.code)
		})
	}
}

// deliverCapture sends a capture reply and waits until the display
// goroutine has run it, so a following Roundtrip sees its events.
func deliverCapture(t *testing.T, s *Server, replies chan<- ports.CaptureDone, done ports.CaptureDone) {
	t.Helper()
	replies <- done
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending := false
		if !s.display.Do(func() { _, pending = s.captureReplies[done.ID] }) {
			t.Fatal("display stopped")
		}
		if !pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("capture reply not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

// applyOutputs sends a layout and waits until the display goroutine has
// applied it, so a following Roundtrip sees its events. A no-op Do alone can
// run before the command goroutine has even received the command.
func applyOutputs(t *testing.T, s *Server, commands chan<- ports.ClientCommand, c ports.SetOutputs) {
	t.Helper()
	commands <- c
	deadline := time.Now().Add(2 * time.Second)
	for {
		applied := false
		if !s.display.Do(func() {
			applied = len(s.outputs) == len(c.Outputs) && slices.Equal(s.outputPlaces, c.Outputs)
		}) {
			t.Fatal("display stopped")
		}
		if applied {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("outputs not applied")
		}
		time.Sleep(time.Millisecond)
	}
}

// captureDetails records protocol payloads, not just the event opcodes.
type captureDetails struct {
	wlturbo.BaseProxy
	events chan []uint32
}

func (p *captureDetails) Dispatch(e *wlturbo.Event) {
	n := 0
	switch e.Opcode {
	case 0:
		n = 2
	case 1:
		n = 1
		if len(e.Data()) >= 16 {
			n = 4
		}
	case 2:
		n = 3
	case 4:
		if len(e.Data()) >= 4 {
			n = 1
		}
	}
	values := []uint32{uint32(e.Opcode)}
	for i := 0; i < n; i++ {
		values = append(values, e.Uint32())
	}
	p.events <- values
}
func TestExtCaptureLifecycle(t *testing.T) {
	dir := t.TempDir()
	requests := make(chan ports.CaptureRequest, 1)
	replies := make(chan ports.CaptureDone, 1)
	commands := make(chan ports.ClientCommand, 4)
	initial := ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}
	s, err := New(Options{RuntimeDir: dir, CaptureAllow: allowStore(t, "*\n"), Outputs: ports.Layout{initial}}, Channels{Captures: requests, Captured: replies, Commands: commands}, logging.For(context.Background(), "wayland"))
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
	g, ok := c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("output missing")
	}
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	srcMgr := bindProtocol(t, c, "ext_output_image_capture_source_manager_v1")
	mgr := bindProtocol(t, c, "ext_image_copy_capture_manager_v1")
	src, session := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, src)
	p := &captureDetails{events: make(chan []uint32, 16)}
	p.SetID(session)
	registerWireProxy(c, p)
	requestProtocol(t, c, srcMgr, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, out)
	requestProtocol(t, c, mgr, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, session, src, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]uint32{{0, 4, 4}, {1, uint32(wayland.ShmFormatXrgb8888)}, {1, uint32(wayland.ShmFormatArgb8888)}, {4}} {
		got := <-p.events
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("session event %v != %v", got, want)
		}
	}
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("ext-capture", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// Room for the 4x4 and the resized 5x4 buffers.
	if err := unix.Ftruncate(fd, 80); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(80)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(4), int32(4), int32(16), uint32(wayland.ShmFormatXrgb8888))
	frame := c.AllocateID()
	fp := &captureDetails{events: make(chan []uint32, 16)}
	fp.SetID(frame)
	registerWireProxy(c, fp)
	requestProtocol(t, c, session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame)
	requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, buf)
	requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var req ports.CaptureRequest
	select {
	case req = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("no capture request")
	}
	if req.Width != 4 || req.Height != 4 {
		t.Fatalf("request %+v", req)
	}
	if err := req.Dst.File.Close(); err != nil {
		t.Fatal(err)
	}
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	deliverCapture(t, s, replies, ports.CaptureDone{ID: req.ID, Time: time.Unix(now.Sec, now.Nsec)})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]uint32{{0, 0, 0}, {1, 0, 0, 4, 4}, {2, 0, uint32(now.Sec), uint32(now.Nsec)}, {3}} {
		got := <-fp.events
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frame event %v != %v", got, want)
		}
	}
	resized := initial
	resized.Info.Width = 5
	resized.Width = 5
	applyOutputs(t, s, commands, ports.SetOutputs{Outputs: ports.Layout{resized}})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// A resize sends a complete constraint batch, formats included.
	for _, want := range [][]uint32{{0, 5, 4}, {1, uint32(wayland.ShmFormatXrgb8888)}, {1, uint32(wayland.ShmFormatArgb8888)}, {4}} {
		got := <-p.events
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resize event %v != %v", got, want)
		}
	}
	// Destroying the session leaves its pending frame alive: it still gets ready.
	requestProtocol(t, c, frame, ext.ExtImageCopyCaptureFrameV1RequestDestroy) // one live frame per session
	buf2, frame2 := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, buf2)
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf2, int32(0), int32(5), int32(4), int32(20), uint32(wayland.ShmFormatXrgb8888))
	fp2 := &captureDetails{events: make(chan []uint32, 16)}
	fp2.SetID(frame2)
	registerWireProxy(c, fp2)
	requestProtocol(t, c, session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame2)
	requestProtocol(t, c, frame2, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, buf2)
	requestProtocol(t, c, frame2, ext.ExtImageCopyCaptureFrameV1RequestCapture)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case req = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("no capture request after resize")
	}
	if err := req.Dst.File.Close(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, session, ext.ExtImageCopyCaptureSessionV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deliverCapture(t, s, replies, ports.CaptureDone{ID: req.ID, Time: time.Unix(now.Sec, now.Nsec)})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]uint32{{0, 0, 0}, {1, 0, 0, 5, 4}, {2, 0, uint32(now.Sec), uint32(now.Nsec)}, {3}} {
		select {
		case got := <-fp2.events:
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("frame after session destroy: %v != %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame after session destroy: missing %v", want)
		}
	}
}

// The remaining stop check needs a live session.
func TestExtCaptureStoppedOnOutputRemoval(t *testing.T) {
	dir := t.TempDir()
	commands := make(chan ports.ClientCommand, 4)
	initial := ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}
	s, err := New(Options{RuntimeDir: dir, CaptureAllow: allowStore(t, "*\n"), Outputs: ports.Layout{initial}}, Channels{Captures: make(chan ports.CaptureRequest, 1), Captured: make(chan ports.CaptureDone, 1), Commands: commands}, logging.For(context.Background(), "wayland"))
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
	g, ok := c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("output missing")
	}
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	srcMgr := bindProtocol(t, c, "ext_output_image_capture_source_manager_v1")
	mgr := bindProtocol(t, c, "ext_image_copy_capture_manager_v1")
	src, session := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, src)
	p := &captureDetails{events: make(chan []uint32, 16)}
	p.SetID(session)
	registerWireProxy(c, p)
	requestProtocol(t, c, srcMgr, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, out)
	requestProtocol(t, c, mgr, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, session, src, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for range 4 { // initial constraint batch
		<-p.events
	}
	applyOutputs(t, s, commands, ports.SetOutputs{})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := <-p.events; !reflect.DeepEqual(got, []uint32{5}) {
		t.Fatalf("stopped: %v", got)
	}
}

func TestWlrOutputGoneAndDestroyedBeforeReply(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint("destroy=", destroy), func(t *testing.T) {
			dir := t.TempDir()
			requests := make(chan ports.CaptureRequest, 1)
			replies := make(chan ports.CaptureDone, 1)
			commands := make(chan ports.ClientCommand, 2)
			initial := ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 2, Height: 2}, Width: 2, Height: 2, Scale: 1}
			s, err := New(Options{RuntimeDir: dir, CaptureAllow: allowStore(t, "*\n"), Outputs: ports.Layout{initial}}, Channels{Captures: requests, Captured: replies, Commands: commands}, logging.For(context.Background(), "wayland"))
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
			g, ok := c.Registry().FindGlobal("wl_output")
			if !ok {
				t.Fatal("output missing")
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
			requestProtocol(t, c, manager, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), out)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if got := <-events.events; got != uint16(wlr.ZwlrScreencopyFrameV1EventBuffer) {
				t.Fatal(got)
			}
			if got := <-events.events; got != uint16(wlr.ZwlrScreencopyFrameV1EventBufferDone) {
				t.Fatal(got)
			}
			if !destroy {
				applyOutputs(t, s, commands, ports.SetOutputs{})
				shm, buf, fd := captureSmallBuffer(t, c)
				defer unix.Close(fd)
				_ = shm
				requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				if got := <-events.events; got != uint16(wlr.ZwlrScreencopyFrameV1EventFailed) {
					t.Fatal(got)
				}
				return
			}
			_, buf, fd := captureSmallBuffer(t, c)
			defer unix.Close(fd)
			requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			var req ports.CaptureRequest
			select {
			case req = <-requests:
			case <-time.After(2 * time.Second):
				t.Fatal("no request")
			}
			requestProtocol(t, c, frame, wlr.ZwlrScreencopyFrameV1RequestDestroy)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if !s.display.Do(func() {
				if _, ok := s.captureReplies[req.ID]; ok {
					t.Error("reply retained after frame destroy")
				}
			}) {
				t.Fatal("display stopped")
			}
			if err := req.Dst.File.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := req.Dst.File.Stat(); err == nil {
				t.Fatal("output descriptor open")
			}
			deliverCapture(t, s, replies, ports.CaptureDone{ID: req.ID})
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func captureSmallBuffer(t *testing.T, c *wlturbo.Display) (uint32, uint32, int) {
	t.Helper()
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("small-capture", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Ftruncate(fd, 16); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(2), int32(2), int32(8), uint32(wayland.ShmFormatXrgb8888))
	return shm, buf, fd
}
