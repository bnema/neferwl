package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/cursorshape"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

func cursorServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, chan ports.CursorChange, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	cursors := make(chan ports.CursorChange, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events, Commands: commands, Cursors: cursors}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, events, commands, cursors, dir
}

// nextCursor returns the latest cursor change sent within a short wait.
func nextCursor(t *testing.T, cursors <-chan ports.CursorChange) ports.CursorChange {
	t.Helper()
	var c ports.CursorChange
	select {
	case c = <-cursors:
	case <-time.After(2 * time.Second):
		t.Fatal("no cursor change")
	}
	for {
		select {
		case c = <-cursors:
		case <-time.After(50 * time.Millisecond):
			return c
		}
	}
}

func TestCursorShapeFollowsPointerFocus(t *testing.T) {
	s, events, commands, cursors, dir := cursorServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	manager := bindProtocol(t, c, "wp_cursor_shape_manager_v1")
	device := c.AllocateID()
	registerProtocol(t, c, device)
	requestProtocol(t, c, manager, cursorshape.WpCursorShapeManagerV1RequestGetPointer, device, pointer)
	w := toplevelMapper(t, c, events)()

	// Without pointer focus the request is ignored.
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, uint32(1), uint32(cursorshape.WpCursorShapeDeviceV1ShapeText))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-cursors:
		t.Fatalf("cursor change without focus: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	commands <- ports.PointerFocus{ID: w.ID, X: 0, Y: 0}
	if got := nextCursor(t, cursors); got != (ports.CursorChange{}) {
		t.Fatalf("enter: %+v, want the arrow", got)
	}
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, uint32(2), uint32(cursorshape.WpCursorShapeDeviceV1ShapePointer))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := nextCursor(t, cursors); got.Shape != "pointer" {
		t.Fatalf("shape: %+v", got)
	}
	commands <- ports.PointerFocus{ID: 0}
	if got := nextCursor(t, cursors); got != (ports.CursorChange{}) {
		t.Fatalf("leave: %+v, want the arrow", got)
	}
}

func TestCursorShapeNames(t *testing.T) {
	for shape, want := range map[cursorshape.WpCursorShapeDeviceV1Shape]string{
		cursorshape.WpCursorShapeDeviceV1ShapeDefault:      "default",
		cursorshape.WpCursorShapeDeviceV1ShapeNwseResize:   "nwse-resize",
		cursorshape.WpCursorShapeDeviceV1ShapeZoomOut:      "zoom-out",
		cursorshape.WpCursorShapeDeviceV1ShapeAllResize:    "all-resize",
		cursorshape.WpCursorShapeDeviceV1ShapeVerticalText: "vertical-text",
	} {
		if got := cursorShapes[shape]; got != want {
			t.Errorf("shape %d = %q, want %q", shape, got, want)
		}
	}
}

func TestCursorShapeInvalid(t *testing.T) {
	s, _, _, _, dir := cursorServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	manager := bindProtocol(t, c, "wp_cursor_shape_manager_v1")
	device := c.AllocateID()
	registerProtocol(t, c, device)
	requestProtocol(t, c, manager, cursorshape.WpCursorShapeManagerV1RequestGetPointer, device, pointer)
	// Version 1 ends at zoom_out.
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, uint32(1), uint32(cursorshape.WpCursorShapeDeviceV1ShapeDndAsk))
	expectProtocolError(t, c, device, uint32(cursorshape.WpCursorShapeDeviceV1ErrorInvalidShape))
}

func TestSetCursorSurface(t *testing.T) {
	s, events, commands, cursors, dir := cursorServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	w := toplevelMapper(t, c, events)()
	commands <- ports.PointerFocus{ID: w.ID, X: 0, Y: 0}
	nextCursor(t, cursors)

	comp := bindProtocol(t, c, "wl_compositor")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("cursor", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// 2x1 ARGB: opaque blue, then transparent; stride padded to 12.
	if _, err := unix.Pwrite(fd, []byte{255, 0, 0, 255, 0, 0, 0, 0, 9, 9, 9, 9}, 0); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(12)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(2), int32(1), int32(12), uint32(wayland.ShmFormatArgb8888))
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)

	// A cursor surface without a buffer hides the cursor.
	requestProtocol(t, c, pointer, wayland.PointerRequestSetCursor, uint32(1), surf, int32(1), int32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := nextCursor(t, cursors); !got.Hidden {
		t.Fatalf("empty surface: %+v", got)
	}
	// A commit updates the image.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	got := nextCursor(t, cursors)
	if got.Image == nil || got.Scale != 1 || got.Image.W != 2 || got.Image.H != 1 || got.Image.HotX != 1 || string(got.Image.Pixels) != string([]byte{255, 0, 0, 255, 0, 0, 0, 0}) {
		t.Fatalf("image: %+v %+v", got, got.Image)
	}
	// A null surface hides the cursor.
	requestProtocol(t, c, pointer, wayland.PointerRequestSetCursor, uint32(1), uint32(0), int32(0), int32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := nextCursor(t, cursors); !got.Hidden {
		t.Fatalf("null surface: %+v", got)
	}
}

func TestSetCursorRoleError(t *testing.T) {
	s, events, _, _, dir := cursorServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	toplevelMapper(t, c, events)()
	// Surface 3 of the mapper's connection is the toplevel's wl_surface:
	// find it through the server instead of guessing ids.
	var surf uint32
	s.display.Do(func() {
		for _, st := range s.surfaces {
			if st.kind == roleXDG {
				surf = st.wl.ID()
			}
		}
	})
	requestProtocol(t, c, pointer, wayland.PointerRequestSetCursor, uint32(1), surf, int32(0), int32(0))
	expectProtocolError(t, c, pointer, uint32(wayland.PointerErrorRole))
}
