package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

// A parent's dependency is fixed at the commit request, not when its
// acquire point fires. A later child update cannot slip into that graph.
func TestSubsurfaceAcquireCapturesChildAtRequest(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	c := h.c
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, h.surf)
	registerProtocol(t, c, sub)
	a := shmBuffer(t, c)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, a, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	root := h.dmabuf()
	h.commit(root, 1, 2)
	// This commit came after the parent request, while it was still waiting.
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.content(40 * time.Millisecond); ok && len(got.Children) != 0 {
		t.Fatalf("child published before parent's acquire: %+v", got.Children)
	}
	h.fire(1)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-h.contents:
			if got.ID != h.win || got.DMABuf == nil {
				continue
			}
			if len(got.Children) != 1 || got.Children[0].Width != 1 {
				t.Fatalf("parent captured later child update: %+v", got.Children)
			}
			return
		case <-deadline:
			t.Fatal("parent graph did not publish")
		}
	}
}

// A synchronized child cannot publish until a parent commits; position and
// order are part of that same parent update.
func TestSubsurfaceInitialSyncAndDeferredLayout(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	rootWindow, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	buf := shmBuffer(t, c)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(4), int32(5))
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestPlaceBelow, root)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		t.Fatalf("child/layout published before parent commit: %+v", got)
	case <-time.After(40 * time.Millisecond):
	}
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if got.ID != rootWindow.ID || len(got.Children) != 1 || got.Children[0].X != 4 || got.Children[0].Y != 5 || !got.Children[0].Below {
			t.Fatalf("parent publication: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no parent publication")
	}
	select {
	case got := <-contents:
		t.Fatalf("extra publication: %+v", got)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestSubsurfaceDesyncFlushesOnlyEffectiveTransition(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	parent, child := c.AllocateID(), c.AllocateID()
	for _, id := range []uint32{parent, child} {
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, id)
		registerProtocol(t, c, id)
	}
	parentSub, childSub := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, parentSub, parent, root)
	registerProtocol(t, c, parentSub)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, childSub, child, parent)
	registerProtocol(t, c, childSub)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, parent, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, parent, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, childSub, wayland.SubsurfaceRequestSetDesync)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if len(got.Children) != 0 {
			t.Fatalf("ineffective desync published: %+v", got.Children)
		}
	case <-time.After(40 * time.Millisecond):
	}
	requestProtocol(t, c, parentSub, wayland.SubsurfaceRequestSetDesync)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if len(got.Children) == 2 {
				return
			}
		case <-deadline:
			t.Fatal("desync did not flush inherited child update")
		}
	}
}

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
