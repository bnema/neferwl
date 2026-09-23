package wayland

import (
	"errors"
	"github.com/bnema/nefertty/internal/ports"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

func protocolClient(t *testing.T, s *Server, dir string) *wlturbo.Display {
	t.Helper()
	c, err := wlturbo.Connect(filepath.Join(dir, s.SocketName()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return c
}

func bindProtocol(t *testing.T, c *wlturbo.Display, iface string) uint32 {
	t.Helper()
	g, ok := c.Registry().FindGlobal(iface)
	if !ok {
		t.Fatalf("missing %s", iface)
	}
	id, err := c.Registry().BindID(g.Name, g.Interface, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func requestProtocol(t *testing.T, c *wlturbo.Display, id uint32, op uint32, args ...any) {
	t.Helper()
	if err := c.SendRequest(id, uint16(op), args...); err != nil {
		t.Fatal(err)
	}
}

func expectProtocolError(t *testing.T, c *wlturbo.Display, object, code uint32) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Roundtrip() }()
	select {
	case err := <-done:
		var displayErr *wlturbo.DisplayError
		if !errors.As(err, &displayErr) {
			t.Fatalf("expected display error, got %v", err)
		}
		if displayErr.ObjectID != object || displayErr.Code != code {
			t.Fatalf("protocol error = %v, want object=%d code=%d", displayErr, object, code)
		}
	case <-time.After(2 * time.Second):
		_ = c.Close()
		t.Fatal("protocol error timeout")
	}
}

type protocolProxy struct{ wlturbo.BaseProxy }

func (*protocolProxy) Dispatch(*wlturbo.Event) {}

func registerProtocol(t *testing.T, c *wlturbo.Display, id uint32) {
	t.Helper()
	p := &protocolProxy{}
	p.SetID(id)
	c.Context().Register(p)
}

func TestXDGProtocolErrors(t *testing.T) {
	for _, scenario := range []string{"bogus ack", "buffer before ack", "subsurface role", "defunct toplevel"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			compositor := bindProtocol(t, c, "wl_compositor")
			wm := bindProtocol(t, c, "xdg_wm_base")
			surface := c.AllocateID()
			requestProtocol(t, c, compositor, wayland.CompositorRequestCreateSurface, surface)
			registerProtocol(t, c, surface)
			if scenario == "subsurface role" {
				parent := c.AllocateID()
				requestProtocol(t, c, compositor, wayland.CompositorRequestCreateSurface, parent)
				registerProtocol(t, c, parent)
				subcomp := bindProtocol(t, c, "wl_subcompositor")
				requestProtocol(t, c, subcomp, wayland.SubcompositorRequestGetSubsurface, c.AllocateID(), surface, parent)
				requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, c.AllocateID(), surface)
				expectProtocolError(t, c, wm, uint32(xdgshell.WmBaseErrorRole))
				return
			}
			xdg := c.AllocateID()
			requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surface)
			top := c.AllocateID()
			registerProtocol(t, c, xdg)
			registerProtocol(t, c, top)
			requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
			switch scenario {
			case "bogus ack":
				requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, uint32(0))
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorInvalidSerial))
			case "buffer before ack":
				shm := bindProtocol(t, c, "wl_shm")
				fd, err := unix.MemfdCreate("protocol-buffer", 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				if err := unix.Ftruncate(fd, 4); err != nil {
					t.Fatal(err)
				}
				pool := c.AllocateID()
				registerProtocol(t, c, shm)
				if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
					t.Fatal(err)
				}
				buffer := c.AllocateID()
				registerProtocol(t, c, pool)
				registerProtocol(t, c, buffer)
				requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
				requestProtocol(t, c, surface, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
				requestProtocol(t, c, surface, wayland.SurfaceRequestCommit)
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorUnconfiguredBuffer))
			case "defunct toplevel":
				requestProtocol(t, c, xdg, xdgshell.SurfaceRequestDestroy)
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorDefunctRoleObject))
			}
		})
	}
}

func TestPopupBlocksSecondRole(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	registerProtocol(t, c, xdg)
	registerProtocol(t, c, surf)
	positioner := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	popup := c.AllocateID()
	registerProtocol(t, c, popup)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetPopup, popup, uint32(0), positioner)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, c.AllocateID())
	expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorAlreadyConstructed))
}

type configureProxy struct {
	wlturbo.BaseProxy
	serial chan uint32
}

func (p *configureProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(xdgshell.SurfaceEventConfigure) {
		p.serial <- e.Uint32()
	}
}

func TestRecreatedToplevelRequiresNewBuffer(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 4)
	proxy := &configureProxy{serial: serials}
	proxy.SetID(xdg)
	c.Context().Register(proxy)
	fd, err := unix.MemfdCreate("recreated-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, shm)
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	case <-time.After(2 * time.Second):
		t.Fatal("initial configure timeout")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if _, ok := ev.(ports.WindowUnmapped); !ok {
			t.Fatalf("expected unmap, got %T", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unmap timeout")
	}
	top = c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	case <-time.After(2 * time.Second):
		t.Fatal("recreated configure timeout")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("mapped without new buffer: %T", ev)
	case <-time.After(300 * time.Millisecond):
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
}

func TestSHMInvalidStride(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	shm := bindProtocol(t, c, "wl_shm")
	fd, err := unix.MemfdCreate("invalid-stride", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 16); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, shm)
	registerProtocol(t, c, pool)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, c.AllocateID(), int32(0), int32(2), int32(1), int32(4), uint32(wayland.ShmFormatArgb8888))
	expectProtocolError(t, c, shm, uint32(wayland.ShmErrorInvalidStride))
}
