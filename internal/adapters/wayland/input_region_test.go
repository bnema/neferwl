package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/pointerconstraints"
	"golang.org/x/sys/unix"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

func inputChanged(t *testing.T, events <-chan ports.ClientEvent) ports.InputRegionChanged {
	t.Helper()
	for {
		if v, ok := waitEvent(t, events, 2*time.Second).(ports.InputRegionChanged); ok {
			return v
		}
	}
}

func TestInputRegionCommitCopySubtractAndReset(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	reg := c.AllocateID()
	registerProtocol(t, c, reg)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, reg)
	requestProtocol(t, c, reg, wayland.RegionRequestAdd, int32(0), int32(0), int32(1), int32(1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestSetInputRegion, reg)
	requestProtocol(t, c, reg, wayland.RegionRequestSubtract, int32(0), int32(0), int32(1), int32(1))
	requestProtocol(t, c, reg, wayland.RegionRequestDestroy)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got := inputChanged(t, events); got.ID != w.ID || len(got.Rects) != 1 || got.Rects[0] != (ports.Rect{W: 1, H: 1}) {
		t.Fatalf("copied region: %+v", got)
	}
	empty := c.AllocateID()
	registerProtocol(t, c, empty)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, empty)
	requestProtocol(t, c, surf, wayland.SurfaceRequestSetInputRegion, empty)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got := inputChanged(t, events); got.All || len(got.Rects) != 0 {
		t.Fatalf("empty region: %+v", got)
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestSetInputRegion, uint32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got := inputChanged(t, events); !got.All {
		t.Fatalf("reset region: %+v", got)
	}
}

func TestRegionSubtractSplits(t *testing.T) {
	g := &region{}
	g.Add(nil, 0, 0, 10, 10)
	g.Subtract(nil, 2, 2, 6, 6)
	if len(g.rects) != 4 || g.boxRect() != (ports.Rect{W: 10, H: 10}) {
		t.Fatalf("subtract: %+v", g.rects)
	}
}

func TestSubsurfaceInputRegionOwnCommitAndOffset(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	registerProtocol(t, c, child)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	sub := c.AllocateID()
	registerProtocol(t, c, sub)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	// surfaceMapper owns a one-pixel buffer; construct a second one for the child.
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("input-region-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 64); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(64)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(4), int32(4), int32(16), uint32(0))
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(3), int32(4))
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	got := inputChanged(t, events)
	if len(got.Rects) == 1 {
		got = inputChanged(t, events)
	}
	if got.ID != w.ID || len(got.Rects) != 2 || got.Rects[1] != (ports.Rect{X: 3, Y: 4, W: 4, H: 4}) {
		t.Fatalf("default child input: %+v", got)
	}
	reg := c.AllocateID()
	registerProtocol(t, c, reg)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, reg)
	requestProtocol(t, c, reg, wayland.RegionRequestAdd, int32(1), int32(1), int32(9), int32(9))
	requestProtocol(t, c, child, wayland.SurfaceRequestSetInputRegion, reg)
	requestProtocol(t, c, reg, wayland.RegionRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// No parent commit is needed, but the child's own commit is required.
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	got = inputChanged(t, events)
	if got.ID != w.ID || len(got.Rects) != 2 || got.Rects[1] != (ports.Rect{X: 4, Y: 5, W: 3, H: 3}) {
		t.Fatalf("committed child input: %+v", got)
	}
}

func TestConstraintIntersectsInputRegion(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	w, surf, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	region := c.AllocateID()
	registerProtocol(t, c, region)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, region)
	requestProtocol(t, c, region, wayland.RegionRequestAdd, int32(0), int32(0), int32(1), int32(1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestSetInputRegion, region)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	_ = inputChanged(t, events)
	constraints := bindProtocol(t, c, "zwp_pointer_constraints_v1")
	confine := c.AllocateID()
	registerProtocol(t, c, confine)
	requestProtocol(t, c, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestConfinePointer, confine, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	commands <- ports.FocusWindow{ID: w.ID}
	commands <- ports.PointerFocus{ID: w.ID}
	commands <- ports.PointerMotionTo{ID: w.ID, X: 0, Y: 0}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := constrained(t, events); got.Rect != (ports.Rect{W: 1, H: 1}) {
		t.Fatalf("constraint intersection: %+v", got)
	}
}
