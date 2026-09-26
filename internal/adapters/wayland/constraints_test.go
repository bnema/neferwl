package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/pointerconstraints"
	"github.com/bnema/purego-libwayland/protocol/relativepointer"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
)

// eventLog records the opcodes (and first fixed args) a proxy receives.
type eventLog struct {
	wlturbo.BaseProxy
	events chan []float64 // opcode, then fixed args for relative motion
}

func (p *eventLog) Dispatch(e *wlturbo.Event) {
	ev := []float64{float64(e.Opcode)}
	if e.Opcode == uint16(relativepointer.ZwpRelativePointerV1EventRelativeMotion) && e.ProxyID == p.ID() {
		e.Uint32()
		e.Uint32()
		for range 4 {
			ev = append(ev, e.Fixed().Float64())
		}
	}
	p.events <- ev
}

func logProxy(t *testing.T, c *wlturbo.Display, id uint32) chan []float64 {
	t.Helper()
	p := &eventLog{events: make(chan []float64, 64)}
	p.SetID(id)
	c.Context().Register(p)
	return p.events
}

// next dispatches until the proxy receives an event.
func next(t *testing.T, c *wlturbo.Display, events chan []float64) []float64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			return ev
		default:
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("no event")
	return nil
}

func constrained(t *testing.T, events <-chan ports.ClientEvent) ports.PointerConstrained {
	t.Helper()
	for {
		if v, ok := waitEvent(t, events, 2*time.Second).(ports.PointerConstrained); ok {
			return v
		}
	}
}

func TestPointerLockAndRelativeMotion(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	pointerEvents := logProxy(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	relManager := bindProtocol(t, c, "zwp_relative_pointer_manager_v1")
	rel := c.AllocateID()
	relEvents := logProxy(t, c, rel)
	requestProtocol(t, c, relManager, relativepointer.ZwpRelativePointerManagerV1RequestGetRelativePointer, rel, pointer)
	w, surf, _ := surfaceMapper(t, c, events)()

	constraints := bindProtocol(t, c, "zwp_pointer_constraints_v1")
	lock := c.AllocateID()
	lockEvents := logProxy(t, c, lock)
	requestProtocol(t, c, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestLockPointer, lock, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-lockEvents:
		t.Fatalf("locked without focus: %v", ev)
	default:
	}

	// Pointer and keyboard focus activate the lock.
	commands <- ports.PointerFocus{ID: w.ID}
	commands <- ports.FocusWindow{ID: w.ID}
	if ev := next(t, c, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventLocked) {
		t.Fatalf("lock event %v", ev)
	}
	if got := constrained(t, events); got.ID != w.ID || got.Mode != ports.ConstraintLock {
		t.Fatalf("constrained %+v", got)
	}
	for len(pointerEvents) > 0 {
		<-pointerEvents
	}

	// Locked: relative motion flows, absolute motion does not.
	commands <- ports.PointerMotionTo{ID: w.ID, X: 5, Y: 5, DX: 1.5, DY: -2, UnaccelDX: 3, UnaccelDY: -4}
	if ev := next(t, c, relEvents); len(ev) != 5 || ev[1] != 1.5 || ev[2] != -2 || ev[3] != 3 || ev[4] != -4 {
		t.Fatalf("relative motion %v", ev)
	}
	for len(pointerEvents) > 0 {
		if ev := <-pointerEvents; ev[0] == float64(wayland.PointerEventMotion) {
			t.Fatal("motion sent to a locked pointer")
		}
	}

	// Losing focus unlocks; a persistent lock comes back.
	commands <- ports.FocusWindow{}
	if ev := next(t, c, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventUnlocked) {
		t.Fatalf("unlock event %v", ev)
	}
	if got := constrained(t, events); got.ID != 0 {
		t.Fatalf("constrained %+v", got)
	}
	commands <- ports.FocusWindow{ID: w.ID}
	if ev := next(t, c, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventLocked) {
		t.Fatalf("relock event %v", ev)
	}
}

func TestPointerConfineOneshotRegion(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	pointerEvents := logProxy(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	comp := bindProtocol(t, c, "wl_compositor")
	w, surf, xdgID := surfaceMapper(t, c, events)()
	region := c.AllocateID()
	registerProtocol(t, c, region)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, region)
	requestProtocol(t, c, region, wayland.RegionRequestAdd, int32(2), int32(3), int32(10), int32(10))
	requestProtocol(t, c, region, wayland.RegionRequestAdd, int32(20), int32(3), int32(5), int32(20))

	constraints := bindProtocol(t, c, "zwp_pointer_constraints_v1")
	confine := c.AllocateID()
	confineEvents := logProxy(t, c, confine)
	requestProtocol(t, c, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestConfinePointer, confine, surf, pointer, region, uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimeOneshot))
	// The region is copied: changing it later has no effect.
	requestProtocol(t, c, region, wayland.RegionRequestAdd, int32(0), int32(0), int32(100), int32(100))
	// Outside the region the constraint waits for the pointer.
	commands <- ports.FocusWindow{ID: w.ID}
	commands <- ports.PointerFocus{ID: w.ID}
	commands <- ports.PointerMotionTo{ID: w.ID, X: 50, Y: 50}
	// Sync on the motion reaching the client before checking.
	for ev := next(t, c, pointerEvents); ev[0] != float64(wayland.PointerEventMotion); ev = next(t, c, pointerEvents) {
	}
	if len(confineEvents) > 0 {
		t.Fatal("confined outside the region")
	}
	commands <- ports.PointerMotionTo{ID: w.ID, X: 5, Y: 5}
	if ev := next(t, c, confineEvents); ev[0] != float64(pointerconstraints.ZwpConfinedPointerV1EventConfined) {
		t.Fatalf("confine event %v", ev)
	}
	want := ports.PointerConstrained{ID: w.ID, PointerConstraint: ports.PointerConstraint{Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 2, Y: 3, W: 23, H: 20}}}
	if got := constrained(t, events); got != want {
		t.Fatalf("constrained %+v, want %+v", got, want)
	}

	// A geometry change moves the window-local region.
	requestProtocol(t, c, xdgID, xdgshell.SurfaceRequestSetWindowGeometry, int32(1), int32(1), int32(1), int32(1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got := constrained(t, events); got.Rect != (ports.Rect{X: 1, Y: 2, W: 23, H: 20}) {
		t.Fatalf("after geometry %+v", got)
	}

	// A new region applies on commit.
	requestProtocol(t, c, confine, pointerconstraints.ZwpConfinedPointerV1RequestSetRegion, uint32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got := constrained(t, events); got.Rect != (ports.Rect{}) {
		t.Fatalf("whole surface %+v", got)
	}

	// A oneshot constraint ends for good with the focus.
	commands <- ports.PointerFocus{}
	if ev := next(t, c, confineEvents); ev[0] != float64(pointerconstraints.ZwpConfinedPointerV1EventUnconfined) {
		t.Fatalf("unconfine event %v", ev)
	}
	commands <- ports.PointerFocus{ID: w.ID, X: 5, Y: 5}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-confineEvents:
		t.Fatalf("oneshot reactivated: %v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// A second constraint on the surface is a protocol error.
	again := c.AllocateID()
	registerProtocol(t, c, again)
	requestProtocol(t, c, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestLockPointer, again, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimeOneshot))
	expectProtocolError(t, c, constraints, uint32(pointerconstraints.ZwpPointerConstraintsV1ErrorAlreadyConstrained))
}

// Destroying an active lock unlocks the pointer in core.
func TestPointerLockDestroyed(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	w, surf, _ := surfaceMapper(t, c, events)()
	constraints := bindProtocol(t, c, "zwp_pointer_constraints_v1")
	lock := c.AllocateID()
	lockEvents := logProxy(t, c, lock)
	requestProtocol(t, c, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestLockPointer, lock, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	commands <- ports.PointerFocus{ID: w.ID}
	commands <- ports.FocusWindow{ID: w.ID}
	next(t, c, lockEvents)
	if got := constrained(t, events); got.Mode != ports.ConstraintLock {
		t.Fatalf("constrained %+v", got)
	}
	requestProtocol(t, c, lock, pointerconstraints.ZwpLockedPointerV1RequestDestroy)
	if got := constrained(t, events); got.ID != 0 {
		t.Fatalf("after destroy %+v", got)
	}
}
