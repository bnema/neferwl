package wayland

import (
	"fmt"
	"testing"

	"github.com/bnema/go-wayland-bindings/server/cursorshape"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// surfacePointer is a real wire client, recording the implicit event target.
type surfacePointer struct {
	wlturbo.BaseProxy
	surface uint32
	serial  uint32
	events  chan string
}

func (p *surfacePointer) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.PointerEventEnter):
		p.serial = e.Uint32()
		p.surface = e.Uint32()
		p.events <- fmt.Sprintf("enter %d %.2f %.2f", p.surface, e.Fixed().Float64(), e.Fixed().Float64())
	case uint16(wayland.PointerEventLeave):
		e.Uint32()
		p.events <- fmt.Sprintf("leave %d", e.Uint32())
		p.surface = 0
	case uint16(wayland.PointerEventMotion):
		e.Uint32()
		p.events <- fmt.Sprintf("motion %d %.2f %.2f", p.surface, e.Fixed().Float64(), e.Fixed().Float64())
	case uint16(wayland.PointerEventButton):
		e.Uint32()
		e.Uint32()
		p.events <- fmt.Sprintf("button %d %d %d", p.surface, e.Uint32(), e.Uint32())
	case uint16(wayland.PointerEventAxis):
		e.Uint32()
		p.events <- fmt.Sprintf("axis %d %d %.2f", p.surface, e.Uint32(), e.Fixed().Float64())
	case uint16(wayland.PointerEventFrame):
		p.events <- "frame"
	}
}

func TestSubsurfacePointerRouting(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, root, xdg := surfaceMapper(t, c, events)()
	seat := bindVersion(t, c, "wl_seat", 8)
	p := &surfacePointer{events: make(chan string, 64)}
	p.SetID(c.AllocateID())
	registerWireProxy(c, p)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, p.ID())
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	add := func() (uint32, uint32) {
		child, sub := c.AllocateID(), c.AllocateID()
		registerProtocol(t, c, child)
		registerProtocol(t, c, sub)
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
		requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
		requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(3), int32(4))
		requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
		requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
		requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
		return child, sub
	}
	child, sub := add()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expect := func(wants ...string) {
		t.Helper()
		for _, want := range wants {
			if got := recvWire(t, c, "surface pointer", p.events); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		}
	}
	enter := func(id uint32, x, y float64) string { return fmt.Sprintf("enter %d %.2f %.2f", id, x, y) }
	motion := func(id uint32, x, y float64) string { return fmt.Sprintf("motion %d %.2f %.2f", id, x, y) }
	leave := func(id uint32) string { return fmt.Sprintf("leave %d", id) }
	move := func(x, y float64) { commands <- ports.PointerMotionTo{ID: w.ID, X: x, Y: y} }
	move(0.25, 0.5)
	expect(enter(root, .25, .5), "frame", motion(root, .25, .5), "frame")
	move(3.25, 4.5)
	expect(leave(root), "frame", enter(child, .25, .5), "frame", motion(child, .25, .5), "frame")
	commands <- ports.PointerButtonTo{ID: w.ID, Button: 272, Pressed: true, Grab: true}
	expect(fmt.Sprintf("button %d 272 1", child), "frame")
	move(.25, .5)
	expect(motion(child, -2.75, -3.5), "frame")
	commands <- ports.PointerButtonTo{ID: w.ID, Button: 272}
	expect(fmt.Sprintf("button %d 272 0", child), "frame", leave(child), "frame", enter(root, .25, .5), "frame")
	move(.25, .5)
	expect(motion(root, .25, .5), "frame")
	other, _ := add()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(3.25, 4.5)
	expect(leave(root), "frame", enter(other, .25, .5), "frame", motion(other, .25, .5), "frame")
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestPlaceAbove, other)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(3.25, 4.5)
	expect(leave(other), "frame", enter(child, .25, .5), "frame", motion(child, .25, .5), "frame")
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestPlaceBelow, other)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(3.25, 4.5)
	expect(leave(child), "frame", enter(other, .25, .5), "frame", motion(other, .25, .5), "frame")
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestPlaceAbove, other)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(3.25, 4.5)
	expect(leave(other), "frame", enter(child, .25, .5), "frame", motion(child, .25, .5), "frame")
	requestProtocol(t, c, child, wayland.SurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expect(leave(child), "frame")
	move(3.25, 4.5)
	expect(enter(other, .25, .5), "frame", motion(other, .25, .5), "frame")
	// Geometry changes translate window coordinates before walking the tree.
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestSetWindowGeometry, int32(1), int32(2), int32(1), int32(1))
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(2.25, 2.5)
	expect(motion(other, .25, .5), "frame")
	// An empty committed child input region falls through to the root.
	reg := c.AllocateID()
	registerProtocol(t, c, reg)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, reg)
	requestProtocol(t, c, other, wayland.SurfaceRequestSetInputRegion, reg)
	requestProtocol(t, c, other, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(2.25, 2.5)
	expect(leave(other), "frame", enter(root, 3.25, 4.5), "frame", motion(root, 3.25, 4.5), "frame")
	requestProtocol(t, c, other, wayland.SurfaceRequestSetInputRegion, uint32(0))
	requestProtocol(t, c, other, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	move(2.25, 2.5)
	expect(leave(root), "frame", enter(other, .25, .5), "frame", motion(other, .25, .5), "frame")
	requestProtocol(t, c, other, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, other, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expect(leave(other), "frame")
	move(2.25, 2.5)
	expect(enter(root, 3.25, 4.5), "frame", motion(root, 3.25, 4.5), "frame")
}

func TestLatePointerAndCursorEnterSerial(t *testing.T) {
	s, events, commands, _, dir := cursorServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	seat := bindVersion(t, c, "wl_seat", 4)
	newPointer := func() *surfacePointer {
		p := &surfacePointer{events: make(chan string, 32)}
		p.SetID(c.AllocateID())
		registerWireProxy(c, p)
		requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, p.ID())
		return p
	}
	p := newPointer()
	commands <- ports.PointerFocus{ID: w.ID, X: .25, Y: .5}
	if got := recvWire(t, c, "enter", p.events); got != fmt.Sprintf("enter %d 0.25 0.50", root) {
		t.Fatal(got)
	}
	rootSerial := p.serial
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child, sub := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, child)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	late := newPointer()
	if got := recvWire(t, c, "late enter", late.events); got != fmt.Sprintf("enter %d 0.25 0.50", root) {
		t.Fatal(got)
	}
	commands <- ports.PointerMotionTo{ID: w.ID, X: .25, Y: .5}
	for _, p := range []*surfacePointer{p, late} {
		for _, want := range []string{fmt.Sprintf("leave %d", root), fmt.Sprintf("enter %d 0.25 0.50", child), fmt.Sprintf("motion %d 0.25 0.50", child)} {
			if got := recvWire(t, c, "crossing", p.events); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		}
	}
	// v4 must not receive frames. Roundtrip is the wire barrier.
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*surfacePointer{p, late} {
		select {
		case got := <-p.events:
			t.Fatalf("unexpected v4 event %s", got)
		default:
		}
	}
	commands <- ports.PointerAxisTo{ID: w.ID, Axis: ports.PointerAxis{Vertical: domain.ScrollAxis{Set: true, Value: 5}}}
	for _, p := range []*surfacePointer{p, late} {
		if got := recvWire(t, c, "axis", p.events); got != fmt.Sprintf("axis %d 0 5.00", child) {
			t.Fatal(got)
		}
	}
	manager := bindProtocol(t, c, "wp_cursor_shape_manager_v1")
	device := c.AllocateID()
	registerProtocol(t, c, device)
	requestProtocol(t, c, manager, cursorshape.WpCursorShapeManagerV1RequestGetPointer, device, p.ID())
	check := func(hidden bool, shape string) {
		t.Helper()
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		s.display.Do(func() {
			if s.cursorClient.Hidden != hidden || s.cursorClient.Shape != shape {
				t.Errorf("cursor %+v want hidden=%v shape=%s", s.cursorClient, hidden, shape)
			}
		})
	}
	requestProtocol(t, c, p.ID(), wayland.PointerRequestSetCursor, rootSerial, uint32(0), int32(0), int32(0))
	check(false, "")
	requestProtocol(t, c, p.ID(), wayland.PointerRequestSetCursor, p.serial, uint32(0), int32(0), int32(0))
	check(true, "")
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, rootSerial, uint32(cursorshape.WpCursorShapeDeviceV1ShapeText))
	check(true, "")
	// A serial from a different pointer is not authority for this device.
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, late.serial, uint32(cursorshape.WpCursorShapeDeviceV1ShapeText))
	check(true, "")
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, p.serial, uint32(cursorshape.WpCursorShapeDeviceV1ShapeText))
	check(false, "text")
}

func TestSubsurfaceGrabFinalRelease(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	seat := bindVersion(t, c, "wl_seat", 8)
	p := &surfacePointer{events: make(chan string, 64)}
	p.SetID(c.AllocateID())
	registerWireProxy(c, p)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, p.ID())
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	add := func(x int32) uint32 {
		child, sub := c.AllocateID(), c.AllocateID()
		registerProtocol(t, c, child)
		registerProtocol(t, c, sub)
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
		requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
		requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, x, int32(4))
		requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
		requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
		requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
		return child
	}
	a, b := add(3), add(6)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expect := func(wants ...string) {
		t.Helper()
		for _, want := range wants {
			if got := recvWire(t, c, "grab", p.events); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		}
	}
	button := func(code uint32, pressed bool) {
		commands <- ports.PointerButtonTo{ID: w.ID, Button: code, Pressed: pressed, Grab: pressed}
	}
	commands <- ports.PointerMotionTo{ID: w.ID, X: 3.25, Y: 4.5}
	expect(fmt.Sprintf("enter %d 0.25 0.50", a), "frame", fmt.Sprintf("motion %d 0.25 0.50", a), "frame")
	button(272, true)
	expect(fmt.Sprintf("button %d 272 1", a), "frame")
	button(273, true)
	expect(fmt.Sprintf("button %d 273 1", a), "frame")
	commands <- ports.PointerMotionTo{ID: w.ID, X: 6.25, Y: 4.5}
	expect(fmt.Sprintf("motion %d 3.25 0.50", a), "frame")
	button(272, false)
	expect(fmt.Sprintf("button %d 272 0", a), "frame")
	// The remaining held button preserves the pin, including another press.
	button(274, true)
	expect(fmt.Sprintf("button %d 274 1", a), "frame")
	button(274, false)
	expect(fmt.Sprintf("button %d 274 0", a), "frame")
	button(273, false)
	expect(fmt.Sprintf("button %d 273 0", a), "frame", fmt.Sprintf("leave %d", a), "frame", fmt.Sprintf("enter %d 0.25 0.50", b), "frame")
	button(272, true)
	expect(fmt.Sprintf("button %d 272 1", b), "frame")
	button(272, false)
	expect(fmt.Sprintf("button %d 272 0", b), "frame")
}
