package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

// A toplevel with a subsurface (how Firefox draws with the GPU) sends one
// content: its own buffer, the child at its committed position and the
// window geometry.
func TestSubsurfaceContent(t *testing.T) {
	s, events, contents, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("subsurface", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4*4*4); err != nil {
		t.Fatal(err)
	}
	pool, big, small := c.AllocateID(), c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, big)
	registerProtocol(t, c, small)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(64)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, big, int32(0), int32(4), int32(4), int32(16), uint32(0))
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, small, int32(0), int32(2), int32(2), int32(16), uint32(0))

	root, child := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, root)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, root)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	registerProtocol(t, c, sub)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	xdg := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, root)
	serials := make(chan uint32, 4)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	c.Context().Register(xp)
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	case <-time.After(2 * time.Second):
		t.Fatal("configure timeout")
	}
	// Child first: nothing to draw until the root maps.
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(1), int32(2))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, small, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestSetWindowGeometry, int32(1), int32(1), int32(2), int32(2))
	requestProtocol(t, c, root, wayland.SurfaceRequestAttach, big, int32(0), int32(0))
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	w := mapped(t, events, 2*time.Second)
	var got ports.SurfaceContent
	deadline := time.After(2 * time.Second)
	for got.ID == 0 {
		select {
		case got = <-contents:
		case <-deadline:
			t.Fatal("no content")
		}
	}
	if got.ID != w.ID || got.Width != 4 || got.Geometry != (ports.Rect{X: 1, Y: 1, W: 2, H: 2}) {
		t.Fatalf("root %+v", got)
	}
	if len(got.Children) != 1 || got.Children[0].X != 1 || got.Children[0].Y != 2 || got.Children[0].Width != 2 || got.Children[0].Below {
		t.Fatalf("children %+v", got.Children)
	}
	// Destroying the subsurface redraws the root without it.
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline = time.After(2 * time.Second)
	for {
		select {
		case got = <-contents:
			if len(got.Children) == 0 {
				return
			}
		case <-deadline:
			t.Fatalf("child kept: %+v", got.Children)
		}
	}
}

// A surface cannot become a subsurface of its own child.
func TestSubsurfaceLoop(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	a, b := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, a)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, b)
	s1 := c.AllocateID()
	registerProtocol(t, c, s1)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, s1, b, a)
	s2 := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, s2, a, b)
	expectProtocolError(t, c, subc, uint32(wayland.SubcompositorErrorBadParent))
}

// A wl_subsurface outlives its parent: the surface keeps its role, so a
// second get_subsurface is bad_surface.
func TestSubsurfaceRoleOutlivesParent(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	a, b := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, a)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, b)
	registerProtocol(t, c, a)
	s1 := c.AllocateID()
	registerProtocol(t, c, s1)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, s1, b, a)
	requestProtocol(t, c, a, wayland.SurfaceRequestDestroy)
	p := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, p)
	s2 := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, s2, b, p)
	expectProtocolError(t, c, subc, uint32(wayland.SubcompositorErrorBadSurface))
}
