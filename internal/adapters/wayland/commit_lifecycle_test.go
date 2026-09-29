package wayland

import (
	"reflect"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/fifo"
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
)

// Applying an older captured commit must never transiently own newer requests.
// The next commit captures those requests without losing its one-shot state.
func TestCapturedCommitKeepsLaterRequests(t *testing.T) {
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}, frameReady: make(chan struct{}, 1), constraints: map[*surface]*constraint{}}
	surf := &surface{server: srv, xdg: &xdgSurface{}}
	cons := &constraint{surface: surf}
	srv.constraints[surf] = cons
	firstRegion := &ports.Rect{W: 2}
	cons.pending = firstRegion
	surf.next.scale, surf.next.async, surf.next.kind = 2, true, 1
	surf.xdg.pendingGeometry = ports.Rect{W: 10}
	surf.next.inputSet, surf.next.inputAll = true, true
	surf.next.damage = []ports.Rect{{W: 2}}
	surf.next.barrier = true
	surf.queueUpdate()

	laterRegion := &ports.Rect{W: 3}
	cons.pending = laterRegion
	surf.next.scale, surf.next.async, surf.next.kind = 3, false, 2
	surf.sub.pendingLayout = []childLayout{{x: 7}}
	surf.xdg.pendingGeometry = ports.Rect{W: 20}
	surf.next.inputSet, surf.next.inputAll = true, false
	surf.next.inputRects = []ports.Rect{{W: 3}}
	surf.next.damage = []ports.Rect{{W: 3}}
	surf.next.barrier = true
	laterTime := time.Now().Add(time.Second)
	surf.next.at = laterTime

	surf.queue[0].applyGraph()
	if surf.bufferScale != 2 || !surf.async || surf.contentKind != 1 || len(surf.sub.layout) != 0 || surf.xdg.geometry.W != 10 || cons.region.W != 2 || !surf.inputAll || !surf.barrier {
		t.Fatalf("captured state changed: scale=%d async=%v kind=%d geometry=%v region=%v input=%v barrier=%v", surf.bufferScale, surf.async, surf.contentKind, surf.xdg.geometry, cons.region, surf.inputAll, surf.barrier)
	}
	if surf.next.scale != 3 || surf.next.async || surf.next.kind != 2 || len(surf.sub.pendingLayout) != 1 || surf.xdg.pendingGeometry.W != 20 || cons.pending != laterRegion || !surf.next.inputSet || surf.next.inputAll || len(surf.next.inputRects) != 1 || len(surf.next.damage) != 1 || !surf.next.barrier || !surf.next.at.Equal(laterTime) {
		t.Fatal("application consumed a later request")
	}
	surf.queueUpdate()
	second := surf.queue[0]
	if second.scale != 3 || second.async || second.kind != 2 || len(second.layout) != 1 || second.geometry.W != 20 || second.region != laterRegion || !second.inputSet || second.inputAll || len(second.inputRects) != 1 || len(second.damage) != 1 || !second.barrier || !second.at.Equal(laterTime) {
		t.Fatal("subsequent commit did not capture later requests")
	}
	surf.dropQueue()
}

// The viewport resource is live, but its requests after capture must not be
// installed by an older queued commit.
func TestCapturedViewportKeepsLaterRequests(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	surfID := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surfID)
	registerProtocol(t, c, surfID)
	vpID := c.AllocateID()
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vpID, surfID)
	registerProtocol(t, c, vpID)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		var surf *surface
		for _, candidate := range s.surfaces {
			if candidate.wl != nil && candidate.wl.ID() == surfID {
				surf = candidate
			}
		}
		if surf == nil || surf.viewport == nil {
			t.Error("missing viewport")
			return
		}
		v := surf.viewport
		v.SetSource(v.resource, 0, 0, server.Fixed(256), server.Fixed(256))
		v.SetDestination(v.resource, 1, 1)
		surf.queueUpdate()
		v.SetSource(v.resource, 0, 0, server.Fixed(512), server.Fixed(512))
		v.SetDestination(v.resource, 2, 2)
		surf.queue[0].applyGraph()
		if surf.committedViewport.destW != 1 || surf.committedViewport.src[2] != 256 || v.pendingW != 2 || v.pendingSrc[2] != 512 {
			t.Errorf("captured=%+v pending=%+v", surf.committedViewport, v)
		}
		surf.queueUpdate()
		surf.queue[0].applyGraph()
		if surf.committedViewport.destW != 2 || surf.committedViewport.src[2] != 512 {
			t.Errorf("next commit: %+v", surf.committedViewport)
		}
	})
}

// Real protocol requests captured behind a barrier keep the first buffer;
// an attach made after capture is owned by the next commit.
func TestQueuedAttachKeepsLaterBuffer(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	fm := bindProtocol(t, c, "wp_fifo_manager_v1")
	id := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, id)
	registerProtocol(t, c, id)
	f := c.AllocateID()
	requestProtocol(t, c, fm, fifo.WpFifoManagerV1RequestGetFifo, f, id)
	registerProtocol(t, c, f)
	first, second := shmBuffer(t, c), shmBuffer(t, c)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestSetBarrier)
	requestProtocol(t, c, id, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, f, fifo.WpFifoV1RequestWaitBarrier)
	requestProtocol(t, c, id, wayland.SurfaceRequestAttach, first, int32(0), int32(0))
	requestProtocol(t, c, id, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, id, wayland.SurfaceRequestAttach, second, int32(0), int32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		var surf *surface
		for _, candidate := range s.surfaces {
			if candidate.wl != nil && candidate.wl.ID() == id {
				surf = candidate
			}
		}
		if surf == nil || len(surf.queue) != 1 || surf.queue[0].buffer.ID() != first || surf.next.buffer.ID() != second {
			t.Error("queued attach lost its captured or later buffer")
			return
		}
		surf.barrier = false
		s.tickFifo(time.Now(), nil)
		if surf.current == nil || surf.current.ID() != first || surf.next.buffer == nil || surf.next.buffer.ID() != second {
			t.Error("applying captured attach consumed later request")
		}
	})
	requestProtocol(t, c, id, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		for _, surf := range s.surfaces {
			if surf.wl != nil && surf.wl.ID() == id && (surf.current == nil || surf.current.ID() != second) {
				t.Error("later attach not applied by next commit")
			}
		}
	})
}

// Layer requests are sticky, but must not be read from live pending state
// when an older queued update is finally applied.
func TestCapturedLayerKeepsLaterRequests(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
	id := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, id)
	registerProtocol(t, c, id)
	layerID := c.AllocateID()
	requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, layerID, id, uint32(0), uint32(ports.LayerTop), "captured")
	registerProtocol(t, c, layerID)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		var surf *surface
		for _, candidate := range s.surfaces {
			if candidate.wl != nil && candidate.wl.ID() == id {
				surf = candidate
			}
		}
		if surf == nil || surf.layer == nil {
			t.Error("missing layer")
			return
		}
		l := surf.layer
		l.SetSize(l.resource, 1, 1)
		l.SetAnchor(l.resource, 1)
		l.SetMargin(l.resource, 1, 2, 3, 4)
		l.SetExclusiveZone(l.resource, 5)
		surf.queueUpdate()
		l.SetSize(l.resource, 2, 2)
		l.SetAnchor(l.resource, 2)
		l.SetMargin(l.resource, 5, 6, 7, 8)
		l.SetExclusiveZone(l.resource, 9)
		surf.queue[0].applyGraph()
		if l.current.width != 1 || l.current.anchor != 1 || l.current.margin != [4]int32{1, 2, 3, 4} || l.current.zone != 5 || l.pending.width != 2 || l.pending.anchor != 2 || l.pending.margin != [4]int32{5, 6, 7, 8} || l.pending.zone != 9 {
			t.Errorf("captured=%+v pending=%+v", l.current, l.pending)
		}
		surf.queueUpdate()
		surf.queue[0].applyGraph()
		if l.current.width != 2 || l.current.anchor != 2 || l.current.zone != 9 {
			t.Errorf("next commit: %+v", l.current)
		}
	})
}

// Role and surface destruction drop their queues. Viewport and buffer
// destruction leave the queued update to apply safely at the next tick.
// Roundtrip orders destruction before examining the display owner.
func TestQueuedDestructionDropsUpdates(t *testing.T) {
	for _, kind := range []string{"viewport", "toplevel", "layer", "surface", "buffer"} {
		t.Run(kind, func(t *testing.T) {
			s, events, _, _, dir := contentServer(t)
			c := protocolClient(t, s, dir)
			var surfID, objectID, bufferID uint32
			if kind == "toplevel" {
				_, surfID, _ = surfaceMapper(t, c, events)()
				s.display.Do(func() {
					for _, surf := range s.surfaces {
						if surf.wl != nil && surf.wl.ID() == surfID {
							objectID = surf.xdg.window.toplevel.ID()
						}
					}
				})
			} else {
				comp := bindProtocol(t, c, "wl_compositor")
				surfID = c.AllocateID()
				requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surfID)
				registerProtocol(t, c, surfID)
				if kind == "viewport" {
					vpm := bindProtocol(t, c, "wp_viewporter")
					objectID = c.AllocateID()
					requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, objectID, surfID)
					registerProtocol(t, c, objectID)
				} else if kind == "layer" {
					shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
					objectID = c.AllocateID()
					requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, objectID, surfID, uint32(0), uint32(ports.LayerTop), "drop")
					registerProtocol(t, c, objectID)
					requestProtocol(t, c, objectID, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(1), uint32(1))
				} else if kind == "buffer" {
					bufferID = shmBuffer(t, c)
				}
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			s.display.Do(func() {
				for _, surf := range s.surfaces {
					if surf.wl != nil && surf.wl.ID() == surfID {
						surf.next.scale = 3
						surf.next.damage = []ports.Rect{{W: 3}}
						if kind == "viewport" {
							surf.viewport.SetDestination(surf.viewport.resource, 2, 2)
						}
						surf.next.at = time.Now().Add(time.Second)
						if kind == "buffer" {
							for resource := range s.buffers {
								if resource.ID() == bufferID {
									surf.next.buffer = wayland.WrapBuffer(resource)
									surf.next.attached = true
								}
							}
						}
						surf.queueUpdate()
					}
				}
			})
			switch kind {
			case "viewport":
				requestProtocol(t, c, objectID, viewporter.WpViewportRequestDestroy)
			case "toplevel":
				requestProtocol(t, c, objectID, xdgshell.ToplevelRequestDestroy)
			case "layer":
				requestProtocol(t, c, objectID, wlrlayershell.ZwlrLayerSurfaceV1RequestDestroy)
			case "surface":
				requestProtocol(t, c, surfID, wayland.SurfaceRequestDestroy)
			case "buffer":
				requestProtocol(t, c, bufferID, wayland.BufferRequestDestroy)
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			s.display.Do(func() {
				for _, surf := range s.surfaces {
					if surf.wl != nil && surf.wl.ID() == surfID {
						if kind == "viewport" || kind == "buffer" {
							if kind == "buffer" && (len(surf.queue) != 1 || surf.queue[0].buffer == nil || surf.queue[0].buffer.Resource.Alive()) {
								t.Error("expected destroyed buffer in queued update")
							}
							if len(surf.queue) != 1 {
								t.Errorf("%s should retain queued update until tick", kind)
							}
							s.tickFifo(time.Now().Add(2*time.Second), nil)
						}
						if len(surf.queue) != 0 {
							t.Errorf("%s retained queued update", kind)
						}
					}
				}
				fresh := &surface{server: s}
				fresh.queueUpdate()
				u := fresh.queue[0]
				if u.scale != 0 || u.buffer != nil || u.vp != nil || u.layer != nil || len(u.damage) != 0 || u.owner != fresh {
					t.Errorf("%s recycled stale update: %+v", kind, u)
				}
				fresh.queue[0].applyGraph()
			})
		})
	}
}

// A dropped update must be cleared before a different surface reuses it.
func TestDroppedUpdateRecycledWithoutState(t *testing.T) {
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}, frameReady: make(chan struct{}, 1)}
	old := &surface{server: srv, next: pendingCommit{scale: 2, barrier: true, damage: []ports.Rect{{W: 2}}}}
	old.queueUpdate()
	old.destroyed = true
	old.dropQueue()
	fresh := &surface{server: srv}
	fresh.queueUpdate()
	if u := fresh.queue[0]; u.scale != 0 || u.barrier || len(u.damage) != 0 || u.owner != fresh || u.buffer != nil || u.vp != nil || u.layer != nil || u.xdg != nil {
		t.Fatalf("recycled update retained old state: %+v", u)
	}
	fresh.queue[0].applyGraph()
}

// BenchmarkEmptySurfaceQueueApply measures an empty, roleless surface's
// in-process queue/apply loop. It excludes protocol dispatch and real buffers.
func BenchmarkEmptySurfaceQueueApply(b *testing.B) {
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}, frameReady: make(chan struct{}, 1)}
	surf := &surface{server: srv}
	surf.queueUpdate()
	surf.queue[0].applyGraph()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		surf.queueUpdate()
		surf.queue[0].applyGraph()
	}
}

// TestCapturedViewportApplyAllocations exercises the real viewport resource
// on the display owner: a viewport-only commit has no heap work.
func TestCapturedViewportApplyAllocations(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	surfID := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surfID)
	registerProtocol(t, c, surfID)
	vpID := c.AllocateID()
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vpID, surfID)
	registerProtocol(t, c, vpID)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		var surf *surface
		for _, candidate := range s.surfaces {
			if candidate.wl != nil && candidate.wl.ID() == surfID {
				surf = candidate
				break
			}
		}
		if surf == nil || surf.viewport == nil || surf.viewport.resource == nil {
			t.Error("missing viewport")
			return
		}
		apply := func() {
			surf.queueUpdate()
			surf.queue[0].applyGraph()
		}
		apply()
		if allocs := testing.AllocsPerRun(100, apply); allocs != 0 {
			t.Errorf("viewport queue/apply: %.1f allocs/commit; want 0", allocs)
		}
	})
}

func TestCapturedCommitApplyAllocations(t *testing.T) {
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}, frameReady: make(chan struct{}, 1)}
	surf := &surface{server: srv}
	apply := func() {
		surf.queueUpdate()
		surf.queue[0].applyGraph()
	}
	apply() // warm pool and queue backing storage
	if allocs := testing.AllocsPerRun(100, apply); allocs != 0 {
		t.Errorf("steady-state queue/apply: %.1f allocs/commit; want 0", allocs)
	}
}

// Capture takes every requested field; only sticky hints carry over.
func TestTakePendingKeepsStickyState(t *testing.T) {
	srv := &Server{constraints: map[*surface]*constraint{}}
	surf := &surface{server: srv}
	all := pendingCommit{
		buffer: &wayland.Buffer{}, attached: true, scale: 2, transform: 1,
		opaque: []ports.Rect{{W: 1}}, opaqueSet: true, inputAll: true, inputSet: true, inputRects: []ports.Rect{{W: 1}},
		callbacks: []*wayland.Callback{{}}, async: true, color: SurfaceColor{Set: true}, representation: surfaceRepresentation{alpha: 1}, kind: 3,
		barrier: true, wait: true, at: time.Unix(1, 0), damage: []ports.Rect{{W: 1}}, bufDamage: []ports.Rect{{W: 1}},
		feedback: []*presentationtime.WpPresentationFeedback{{}}, sync: &commitSync{},
	}
	surf.next = all
	u := surf.takePending()
	if !reflect.DeepEqual(u.pendingCommit, all) {
		t.Fatalf("captured = %+v", u.pendingCommit)
	}
	want := pendingCommit{scale: 2, transform: 1, async: true, color: SurfaceColor{Set: true}, representation: surfaceRepresentation{alpha: 1}, kind: 3}
	if !reflect.DeepEqual(surf.next, want) {
		t.Fatalf("left pending = %+v, want %+v", surf.next, want)
	}
}
