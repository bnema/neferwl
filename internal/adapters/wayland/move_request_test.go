package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// pressSerials records the serial of each pointer button event.
type pressSerials struct {
	wlturbo.BaseProxy
	serials chan uint32
}

func (p *pressSerials) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.PointerEventButton) {
		p.serials <- e.Uint32()
	}
}

func TestToplevelMoveRequest(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	p := &pressSerials{serials: make(chan uint32, 4)}
	p.SetID(pointer)
	registerWireProxy(c, p)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	configured := make(chan uint32, 1)
	xp := &configureProxy{serial: configured}
	xp.SetID(xdg)
	registerWireProxy(c, xp)
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	// Before mapping, a move request is ignored.
	requestProtocol(t, c, top, xdgshell.ToplevelRequestMove, seat, uint32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-configured)
	fd, err := unix.MemfdCreate("move-buffer", 0)
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
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	w := mapped(t, events, 2*time.Second)
	commands <- ports.PointerFocus{ID: w.ID, X: 1, Y: 1}
	commands <- ports.PointerButtonTo{ID: w.ID, Button: 0x110, Pressed: true, Time: 1 * time.Millisecond}
	var serial uint32
	deadline := time.After(2 * time.Second)
	for serial == 0 {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case serial = <-p.serials:
		case <-deadline:
			t.Fatal("no button")
		default:
		}
	}
	// A stale serial emits nothing.
	requestProtocol(t, c, top, xdgshell.ToplevelRequestMove, seat, serial-1)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestResize, seat, serial+7, uint32(xdgshell.ToplevelResizeEdgeRight))
	// The current press starts a move, then a resize.
	requestProtocol(t, c, top, xdgshell.ToplevelRequestMove, seat, serial)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestResize, seat, serial, uint32(xdgshell.ToplevelResizeEdgeBottomLeft))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var got []ports.WindowMoveRequest
	for len(got) < 2 {
		ev := waitEvent(t, events, 2*time.Second)
		if v, ok := ev.(ports.WindowMoveRequest); ok {
			got = append(got, v)
		}
	}
	if got[0] != (ports.WindowMoveRequest{ID: w.ID}) || got[1] != (ports.WindowMoveRequest{ID: w.ID, Resize: true, Edges: ports.ResizeBottom | ports.ResizeLeft}) {
		t.Fatalf("%+v", got)
	}
	select {
	case ev := <-events:
		if _, ok := ev.(ports.WindowMoveRequest); ok {
			t.Fatalf("extra request %+v", ev)
		}
	default:
	}
}

func TestToplevelResizeInvalidEdge(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	surf, xdg, top := c.AllocateID(), c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	registerProtocol(t, c, xdg)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	registerProtocol(t, c, top)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestResize, seat, uint32(1), uint32(3))
	expectProtocolError(t, c, top, uint32(xdgshell.ToplevelErrorInvalidResizeEdge))
}
