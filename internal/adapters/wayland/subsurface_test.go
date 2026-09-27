package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
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

// Desync drains cached commits in FIFO order, including an unmap.
func TestSubsurfaceDesyncQueuedOrderAndUnmap(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	drainContents(contents)
	for _, b := range []uint32{shmBuffer(t, c), shmBuffer(t, c), 0} {
		requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
		requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		t.Fatalf("cached commit published: %+v", got)
	case <-time.After(40 * time.Millisecond):
	}
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetDesync)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.Seq < 5 {
				continue // the forwarder can coalesce intermediate publications
			}
			if got.ID != w.ID || got.Seq != 5 || len(got.Children) != 0 {
				t.Fatalf("desync did not apply all three commits: %+v", got)
			}
			return
		case <-deadline:
			t.Fatal("missing final desync publication")
		}
	}
}

// Pending viewport and scale are captured by the child commit.
func TestSubsurfaceCachedViewportAndScale(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindVersion(t, c, "wl_compositor", 4)
	subc := bindProtocol(t, c, "wl_subcompositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	vp := c.AllocateID()
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, child)
	registerProtocol(t, c, vp)
	b := shmBuffer(t, c)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(128), int32(128))
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(3), int32(4))
	requestProtocol(t, c, child, wayland.SurfaceRequestSetBufferScale, int32(2))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestDamage, int32(0), int32(0), int32(1), int32(1))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(9), int32(9))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if got.ID != w.ID || len(got.Children) != 1 || got.Children[0].LogicalW != 3 || got.Children[0].LogicalH != 4 || got.Children[0].Source != ([4]float32{0, 0, 1, 1}) {
			t.Fatalf("cached viewport: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cached content")
	}
}

// Only the final state of a graph is ever sampled by an output. Buffers
// replaced inside the graph must be released without an output acknowledgement.
func TestSubsurfaceSupersededBufferReleased(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	first := shmBuffer(t, c)
	p := &syncReleaseProxy{released: make(chan struct{}, 4)}
	p.SetID(first)
	c.Context().Register(p)
	for _, b := range []uint32{first, shmBuffer(t, c)} {
		requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
		requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	}
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.released:
	case <-time.After(2 * time.Second):
		t.Fatal("superseded unpublished buffer held for output")
	}
}

// A child update captured by a parent stays with that parent after desync.
func TestSubsurfaceBoundUpdateStaysBound(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetDesync)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if len(got.Children) != 1 {
			t.Fatalf("captured update not applied: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing parent graph")
	}
	select {
	case got := <-contents:
		t.Fatalf("bound update replayed: %+v", got)
	case <-time.After(40 * time.Millisecond):
	}
}

// Destroying the parent cannot publish a captured child dependency.
func TestSubsurfaceParentDestroyDropsCapturedChild(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	_, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// That parent commit legitimately publishes the child: consume it
	// before destroying the parent, so the check below sees only later output.
	select {
	case <-contents:
	case <-time.After(2 * time.Second):
		t.Fatal("parent commit did not publish")
	}
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetDesync)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if len(got.Children) != 0 {
			t.Fatalf("destroyed parent published child: %+v", got)
		}
	case <-time.After(40 * time.Millisecond):
	}
}

// Destroying a queued child removes it from captured parent layouts and
// retires its unpublished buffer without publishing its content.
func TestSubsurfaceDestroyQueuedChild(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	b := shmBuffer(t, c)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, child, wayland.SurfaceRequestDestroy)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.ID == w.ID && len(got.Children) != 0 {
				t.Fatalf("destroyed child published: %+v", got)
			}
			if got.ID == w.ID {
				return
			}
		case <-deadline:
			t.Fatal("no parent update after child destroy")
		}
	}
}

// A queued buffer destroyed before its parent commits is skipped with the
// crop made for it: the retained buffer keeps its own crop, without error.
func TestSubsurfaceDestroyedQueuedBufferKeepsCrop(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	vp := c.AllocateID()
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, child)
	registerProtocol(t, c, vp)
	small, wide := shmBuffer(t, c), shmBuffer(t, c)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, small, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	drainContents(contents)

	// A 2x1 crop meant for a wider buffer that is destroyed while queued.
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(512), int32(256))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, wide, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, wide, wayland.BufferRequestDestroy)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.ID != w.ID {
				continue
			}
			if len(got.Children) != 1 || got.Children[0].Source != ([4]float32{0, 0, 1, 1}) {
				t.Fatalf("retained crop: %+v", got)
			}
			return
		case <-deadline:
			t.Fatal("no parent update")
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

// A parent graph with two child commits only publishes the final child.
func TestSubsurfaceGraphSupersededFeedback(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	s.display.Do(func() { s.windows[w.ID].xdg.window.last.Output = "HEADLESS-1" })
	drainContents(contents)
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	pres := bindProtocol(t, c, "wp_presentation")
	registerProtocol(t, c, pres)
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	first := &feedbackEvents{out: make(chan presentedEvent, 2)}
	id := c.AllocateID()
	first.SetID(id)
	c.Context().Register(first)
	requestProtocol(t, c, pres, presentationtime.WpPresentationRequestFeedback, child, id)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-first.out:
		if !ev.discarded {
			t.Fatalf("superseded content presented: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded feedback not discarded")
	}
}
