package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// transformClient maps a toplevel whose 30×20 argb8888 buffer is shown
// through a viewport, and returns what it needs to commit more state.
type transformClient struct {
	c                  *wlturbo.Display
	comp, surf, vp, bf uint32
	contents           chan ports.SurfaceContent
}

func newTransformClient(t *testing.T) transformClient {
	t.Helper()
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	// set_buffer_transform needs wl_compositor v2.
	g, _ := c.Registry().FindGlobal("wl_compositor")
	comp, err := c.Registry().BindID(g.Name, g.Interface, 6)
	if err != nil {
		t.Fatal(err)
	}
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	vpm := bindProtocol(t, c, "wp_viewporter")
	registerProtocol(t, c, shm)
	surf := c.AllocateID()
	registerProtocol(t, c, surf)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	vp := c.AllocateID()
	registerProtocol(t, c, vp)
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	xdg := c.AllocateID()
	serials := make(chan uint32, 4)
	cp := &configureProxy{serial: serials}
	cp.SetID(xdg)
	c.Context().Register(cp)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	fd, err := unix.MemfdCreate("transform", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	size := int32(30 * 20 * 4)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	pool, bf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, bf)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, size); err != nil {
		t.Fatal(err)
	}
	// argb8888: not opaque by format.
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, bf, int32(0), int32(30), int32(20), int32(120), uint32(wayland.ShmFormatArgb8888))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
	tc := transformClient{c: c, comp: comp, surf: surf, vp: vp, bf: bf, contents: contents}
	if got := tc.next(t); got.Transform != 0 || got.Opaque || got.LogicalW != 30 || got.LogicalH != 20 {
		t.Fatalf("initial content: %+v", got)
	}
	return tc
}

// next returns the next published content with a buffer.
func (tc transformClient) next(t *testing.T) ports.SurfaceContent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-tc.contents:
			if got.SHM != nil {
				return got
			}
		case <-deadline:
			t.Fatal("no content")
		}
	}
}

func (tc transformClient) commit(t *testing.T) {
	t.Helper()
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestCommit)
	if err := tc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// region creates a wl_region holding rects.
func (tc transformClient) region(t *testing.T, rects ...ports.Rect) uint32 {
	t.Helper()
	id := tc.c.AllocateID()
	registerProtocol(t, tc.c, id)
	requestProtocol(t, tc.c, tc.comp, wayland.CompositorRequestCreateRegion, id)
	for _, r := range rects {
		requestProtocol(t, tc.c, id, wayland.RegionRequestAdd, int32(r.X), int32(r.Y), int32(r.W), int32(r.H))
	}
	return id
}

// A 90° buffer transform swaps the surface size and maps the viewport
// source back to buffer pixels; it applies on commit, like the scale.
func TestBufferTransformContent(t *testing.T) {
	tc := newTransformClient(t)
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestSetBufferTransform, int32(1))
	tc.commit(t)
	if got := tc.next(t); got.Transform != 1 || got.Width != 30 || got.Height != 20 || got.LogicalW != 20 || got.LogicalH != 30 {
		t.Fatalf("rotated content: %+v", got)
	}
	// Surface crop x 2..12, y 3..8 of the 20×30 rotated surface. Surface
	// (x, y) is buffer (y, 20-x): buffer x 3..8, y 8..18.
	requestProtocol(t, tc.c, tc.vp, viewporter.WpViewportRequestSetSource, int32(2*256), int32(3*256), int32(10*256), int32(5*256))
	tc.commit(t)
	if got := tc.next(t); got.LogicalW != 10 || got.LogicalH != 5 || got.Source != [4]float32{3, 8, 5, 10} {
		t.Fatalf("rotated crop: %+v", got)
	}
	// A crop that fits the unrotated buffer but not the rotated surface
	// (x up to 25 > 20) is out of buffer.
	requestProtocol(t, tc.c, tc.vp, viewporter.WpViewportRequestSetSource, int32(15*256), int32(0), int32(10*256), int32(5*256))
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestCommit)
	expectProtocolError(t, tc.c, tc.vp, uint32(viewporter.WpViewportErrorOutOfBuffer))
}

func TestBufferTransformInvalid(t *testing.T) {
	tc := newTransformClient(t)
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestSetBufferTransform, int32(8))
	expectProtocolError(t, tc.c, tc.surf, uint32(wayland.SurfaceErrorInvalidTransform))
}

// An opaque region covering the surface marks an alpha buffer opaque; a
// partial or NULL region does not. The region applies on commit.
func TestOpaqueRegionContent(t *testing.T) {
	tc := newTransformClient(t)
	half := tc.region(t, ports.Rect{W: 15, H: 20})
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestSetOpaqueRegion, half)
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestDamage, int32(0), int32(0), int32(1), int32(1))
	tc.commit(t)
	prev := tc.next(t)
	if prev.Opaque {
		t.Fatalf("partial region made the surface opaque: %+v", prev)
	}
	// Two halves cover the whole surface, even through a larger rect.
	// Opacity changes blending everywhere: the commit damages it all.
	whole := tc.region(t, ports.Rect{W: 15, H: 20}, ports.Rect{X: 15, W: 100, H: 20})
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestSetOpaqueRegion, whole)
	tc.commit(t)
	got := tc.next(t)
	if !got.Opaque {
		t.Fatalf("covering region: %+v", got)
	}
	if rects, ok := got.DamageSince(prev.Seq); ok {
		t.Fatalf("opacity change damaged only %v", rects)
	}
	// The surface grows past the region through the viewport: not opaque.
	requestProtocol(t, tc.c, tc.vp, viewporter.WpViewportRequestSetDestination, int32(200), int32(20))
	tc.commit(t)
	if got := tc.next(t); got.Opaque {
		t.Fatalf("region no longer covers: %+v", got)
	}
	requestProtocol(t, tc.c, tc.vp, viewporter.WpViewportRequestSetDestination, int32(-1), int32(-1))
	tc.commit(t)
	if got := tc.next(t); !got.Opaque {
		t.Fatalf("region covers again: %+v", got)
	}
	requestProtocol(t, tc.c, tc.surf, wayland.SurfaceRequestSetOpaqueRegion, uint32(0))
	tc.commit(t)
	if got := tc.next(t); got.Opaque {
		t.Fatalf("NULL region: %+v", got)
	}
}

// Transform and opaque region requested after a queued commit belong to
// the next commit.
func TestCapturedTransformAndOpaqueKeepLaterRequests(t *testing.T) {
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}, frameReady: make(chan struct{}, 1), constraints: map[*surface]*constraint{}}
	surf := &surface{server: srv}
	surf.pendingTransform = 1
	surf.pendingOpaque, surf.pendingOpaqueSet = []ports.Rect{{W: 4, H: 4}}, true
	surf.queueUpdate()
	surf.pendingTransform = 2
	surf.pendingOpaque, surf.pendingOpaqueSet = nil, true
	surf.queue[0].applyGraph()
	if surf.transform != 1 || len(surf.opaque) != 1 {
		t.Fatalf("captured state: transform %d opaque %v", surf.transform, surf.opaque)
	}
	if surf.pendingTransform != 2 || !surf.pendingOpaqueSet || surf.pendingOpaque != nil {
		t.Fatal("application consumed a later request")
	}
	surf.queueUpdate()
	if u := surf.queue[0]; u.transform != 2 || !u.opaqueSet || u.opaque != nil {
		t.Fatalf("next commit: transform %d opaque %v/%v", u.transform, u.opaqueSet, u.opaque)
	}
	surf.dropQueue()
}

func TestCovers(t *testing.T) {
	r := ports.Rect{W: 10, H: 10}
	for _, tc := range []struct {
		name  string
		rects []ports.Rect
		want  bool
	}{
		{"empty", nil, false},
		{"exact", []ports.Rect{{W: 10, H: 10}}, true},
		{"larger", []ports.Rect{{X: -5, Y: -5, W: 30, H: 30}}, true},
		{"two halves", []ports.Rect{{W: 5, H: 10}, {X: 5, W: 5, H: 10}}, true},
		{"gap", []ports.Rect{{W: 4, H: 10}, {X: 5, W: 5, H: 10}}, false},
		{"hole", []ports.Rect{{W: 10, H: 4}, {Y: 6, W: 10, H: 4}, {Y: 4, W: 4, H: 2}, {X: 5, Y: 4, W: 5, H: 2}}, false},
		{"four quarters", []ports.Rect{{W: 5, H: 5}, {X: 5, W: 5, H: 5}, {Y: 5, W: 5, H: 5}, {X: 5, Y: 5, W: 5, H: 5}}, true},
		{"too many", make([]ports.Rect, maxCoverRects+1), false},
	} {
		if got := covers(tc.rects, r); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
