package wayland

import (
	"fmt"
	"testing"
	"time"

	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// A queued attachment that was destroyed cannot apply either its new crop or
// its scale to the buffer retained by a synchronized child.
func TestQueuedDestroyedAttachmentRetainsScaleAndCrop(t *testing.T) {
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
	retained, discarded := shmBuffer(t, c), shmBuffer(t, c)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, retained, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	drainContents(contents)
	requestProtocol(t, c, child, wayland.SurfaceRequestSetBufferScale, int32(2))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, discarded, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, discarded, wayland.BufferRequestDestroy)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.ID != w.ID {
				continue
			}
			if len(got.Children) != 1 || got.Children[0].Source != ([4]float32{0, 0, 1, 1}) || got.Children[0].LogicalW != 1 {
				t.Fatalf("retained buffer changed: %+v", got.Children)
			}
			return
		case <-deadline:
			t.Fatal("retained publication missing")
		}
	}
}

// A scale-only commit validates the retained buffer; same-buffer reattaches
// still advance its version.
func TestScaleOnlyRetainedViewportAndSameBufferReattach(t *testing.T) {
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
	buf := shmBuffer(t, c)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	var first uint64
	deadline := time.After(2 * time.Second)
	for first == 0 {
		select {
		case got := <-contents:
			if got.ID == w.ID && len(got.Children) == 1 {
				first = got.Children[0].Version
			}
		case <-deadline:
			t.Fatal("initial child missing")
		}
	}
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	deadline = time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.ID != w.ID || len(got.Children) != 1 {
				continue
			}
			if got.Children[0].Version != first+1 {
				t.Fatalf("same-buffer version %d, want %d", got.Children[0].Version, first+1)
			}
			goto reattached
		case <-deadline:
			t.Fatal("same-buffer publication missing")
		}
	}
reattached:
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	requestProtocol(t, c, child, wayland.SurfaceRequestSetBufferScale, int32(2))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	expectProtocolError(t, c, vp, uint32(viewporter.WpViewportErrorOutOfBuffer))
}

// A destroyed queued buffer does not suppress the later, valid attachment.
func TestQueuedDestroyedThenNewBuffer(t *testing.T) {
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
	first, destroyed, next := shmBuffer(t, c), shmBuffer(t, c), shmBuffer(t, c)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, first, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	drainContents(contents)
	requestProtocol(t, c, child, wayland.SurfaceRequestSetBufferScale, int32(2))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, destroyed, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, destroyed, wayland.BufferRequestDestroy)
	requestProtocol(t, c, child, wayland.SurfaceRequestSetBufferScale, int32(1))
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, next, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.ID != w.ID || len(got.Children) != 1 {
				continue
			}
			ch := got.Children[0]
			if ch.Version < 2 {
				continue
			}
			if ch.Version != 2 || ch.LogicalW != 1 || ch.Source != ([4]float32{0, 0, 1, 1}) {
				t.Fatalf("later buffer did not apply: %+v", ch)
			}
			return
		case <-deadline:
			t.Fatal("later buffer not published")
		}
	}
}

// A destroyed viewport in an earlier queued update must not replace the new
// viewport or steal its protocol errors.
func TestQueuedViewportRecreatedErrorObject(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized=%v", oversized), func(t *testing.T) {
			s, events, _, contents, dir := contentServer(t)
			c := protocolClient(t, s, dir)
			_, root, _ := surfaceMapper(t, c, events)()
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
			old := c.AllocateID()
			requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, old, child)
			registerProtocol(t, c, old)
			buf := shmBuffer(t, c)
			requestProtocol(t, c, old, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
			requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
			requestProtocol(t, c, child, wayland.SurfaceRequestCommit) // old viewport queued
			requestProtocol(t, c, old, viewporter.WpViewportRequestDestroy)
			current := c.AllocateID()
			requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, current, child)
			registerProtocol(t, c, current)
			width := int32(256)
			if oversized {
				width = 512
			}
			requestProtocol(t, c, current, viewporter.WpViewportRequestSetSource, int32(0), int32(0), width, int32(256))
			requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
			requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
			requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
			if oversized {
				expectProtocolError(t, c, current, uint32(viewporter.WpViewportErrorOutOfBuffer))
				return
			}
			roundtrip(t, c)
			requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
			requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
			roundtrip(t, c)
			deadline := time.After(2 * time.Second)
			for {
				select {
				case got := <-contents:
					if len(got.Children) == 1 && got.Children[0].Source == ([4]float32{0, 0, 1, 1}) {
						return
					}
				case <-deadline:
					t.Fatal("new valid viewport not applied")
				}
			}
		})
	}
}
