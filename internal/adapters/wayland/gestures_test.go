package wayland

import (
	"slices"
	"testing"

	"github.com/bnema/go-wayland-bindings/server/pointergestures"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// gestureLog records the events of a zwp_pointer_gesture_*_v1 object as
// floats: the opcode, then its arguments (fixed values as they are).
type gestureLog struct {
	wlturbo.BaseProxy
	kind   ports.GestureKind
	events chan []float64
}

func (p *gestureLog) Dispatch(e *wlturbo.Event) {
	ev := []float64{float64(e.Opcode)}
	// Hold has no update: its end is opcode 1.
	op := e.Opcode
	if p.kind == ports.GestureHold && op == 1 {
		op = uint16(pointergestures.ZwpPointerGestureSwipeV1EventEnd)
	}
	switch op {
	case uint16(pointergestures.ZwpPointerGestureSwipeV1EventBegin):
		// serial, time, surface, fingers
		for range 4 {
			ev = append(ev, float64(e.Uint32()))
		}
	case uint16(pointergestures.ZwpPointerGestureSwipeV1EventUpdate):
		// time, then dx and dy (and scale and rotation of a pinch)
		fixed := 2
		if p.kind == ports.GesturePinch {
			fixed = 4
		}
		ev = append(ev, float64(e.Uint32()))
		for range fixed {
			ev = append(ev, e.Fixed().Float64())
		}
	case uint16(pointergestures.ZwpPointerGestureSwipeV1EventEnd):
		ev = append(ev, float64(e.Uint32()), float64(e.Uint32()), float64(e.Int32()))
	}
	p.events <- ev
}

func gestureObjects(t *testing.T, c *wlturbo.Display, manager uint32) (swipe, pinch, hold chan []float64) {
	t.Helper()
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	var logs [3]chan []float64
	for i, op := range []uint32{pointergestures.ZwpPointerGesturesV1RequestGetSwipeGesture, pointergestures.ZwpPointerGesturesV1RequestGetPinchGesture, pointergestures.ZwpPointerGesturesV1RequestGetHoldGesture} {
		id := c.AllocateID()
		p := &gestureLog{kind: ports.GestureKind(i), events: make(chan []float64, 16)}
		p.SetID(id)
		registerWireProxy(c, p)
		requestProtocol(t, c, manager, op, id, pointer)
		logs[i] = p.events
	}
	return logs[0], logs[1], logs[2]
}

// quiet fails when the log got an event after a roundtrip.
func quiet(t *testing.T, c *wlturbo.Display, events chan []float64) {
	t.Helper()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %v", ev)
	default:
	}
}

func TestPointerGestures(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	manager := bindVersion(t, c, "zwp_pointer_gestures_v1", 3)
	swipe, pinch, hold := gestureObjects(t, c, manager)
	w, surf, _ := surfaceMapper(t, c, events)()
	// Another client with gestures never has the pointer.
	other := protocolClient(t, s, dir)
	oSwipe, oPinch, oHold := gestureObjects(t, other, bindVersion(t, other, "zwp_pointer_gestures_v1", 3))

	// No gesture reaches a client before it has the pointer.
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GesturePinch, Fingers: 2}
	commands <- ports.PointerFocus{ID: w.ID, X: 10, Y: 10}
	quiet(t, c, pinch)

	// A pinch: begin carries the serial, time, surface and fingers; updates
	// the centre, scale and rotation; the end a later serial.
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GesturePinch, Fingers: 2, Time: 1000 * 1e6}
	begin := next(t, c, pinch)
	if begin[0] != 0 || begin[2] != 1000 || begin[3] != float64(surf) || begin[4] != 2 {
		t.Fatalf("pinch begin %v", begin)
	}
	commands <- ports.GestureUpdateTo{ID: w.ID, Kind: ports.GesturePinch, DX: 1.5, DY: -2, Scale: 1.25, Rotation: 10, Time: 1008 * 1e6}
	if ev := next(t, c, pinch); !slices.Equal(ev, []float64{1, 1008, 1.5, -2, 1.25, 10}) {
		t.Fatalf("pinch update %v", ev)
	}
	commands <- ports.GestureEndTo{ID: w.ID, Kind: ports.GesturePinch, Time: 1016 * 1e6}
	end := next(t, c, pinch)
	if end[0] != 2 || end[1] <= begin[1] || end[2] != 1016 || end[3] != 0 {
		t.Fatalf("pinch end %v (begin %v)", end, begin)
	}

	// A swipe and a hold, the hold ended cancelled.
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GestureSwipe, Fingers: 5, Time: 2000 * 1e6}
	commands <- ports.GestureUpdateTo{ID: w.ID, Kind: ports.GestureSwipe, DX: 3, DY: 4, Time: 2008 * 1e6}
	commands <- ports.GestureEndTo{ID: w.ID, Kind: ports.GestureSwipe, Time: 2016 * 1e6}
	if ev := next(t, c, swipe); ev[0] != 0 || ev[4] != 5 {
		t.Fatalf("swipe begin %v", ev)
	}
	if ev := next(t, c, swipe); !slices.Equal(ev, []float64{1, 2008, 3, 4}) {
		t.Fatalf("swipe update %v", ev)
	}
	if ev := next(t, c, swipe); ev[0] != 2 || ev[3] != 0 {
		t.Fatalf("swipe end %v", ev)
	}
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GestureHold, Fingers: 3, Time: 3000 * 1e6}
	commands <- ports.GestureEndTo{ID: w.ID, Kind: ports.GestureHold, Cancelled: true, Time: 3008 * 1e6}
	if ev := next(t, c, hold); ev[0] != 0 || ev[4] != 3 {
		t.Fatalf("hold begin %v", ev)
	}
	if ev := next(t, c, hold); ev[0] != 1 || ev[3] != 1 {
		t.Fatalf("hold end %v", ev)
	}

	// A gesture of one kind does not reach the objects of the others, and an
	// update of a gesture that is not running is dropped.
	commands <- ports.GestureUpdateTo{ID: w.ID, Kind: ports.GesturePinch, Scale: 2}
	quiet(t, c, pinch)
	quiet(t, c, swipe)
	quiet(t, c, hold)

	// The pointer leaving the window cancels the gesture. Commands run in
	// order: the next pinch event being this begin proves the dropped
	// update above sent nothing.
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GesturePinch, Fingers: 2, Time: 4000 * 1e6}
	if ev := next(t, c, pinch); ev[0] != 0 || ev[2] != 4000 {
		t.Fatalf("pinch begin %v", ev)
	}
	commands <- ports.PointerFocus{}
	if ev := next(t, c, pinch); ev[0] != 2 || ev[3] != 1 {
		t.Fatalf("pinch not cancelled on leave: %v", ev)
	}
	commands <- ports.GestureEndTo{ID: w.ID, Kind: ports.GesturePinch, Time: 4008 * 1e6}
	quiet(t, c, pinch)

	// Releasing the manager keeps its objects.
	commands <- ports.PointerFocus{ID: w.ID, X: 10, Y: 10}
	requestProtocol(t, c, manager, pointergestures.ZwpPointerGesturesV1RequestRelease)
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GestureHold, Fingers: 4, Time: 5000 * 1e6}
	if ev := next(t, c, hold); ev[0] != 0 || ev[4] != 4 {
		t.Fatalf("hold begin after release %v", ev)
	}
	// The end after the cancel sent nothing: the next pinch event is this
	// begin.
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GesturePinch, Fingers: 2, Time: 6000 * 1e6}
	if ev := next(t, c, pinch); ev[0] != 0 || ev[2] != 6000 {
		t.Fatalf("pinch begin after cancelled end %v", ev)
	}

	// The client without the pointer saw nothing.
	quiet(t, other, oSwipe)
	quiet(t, other, oPinch)
	quiet(t, other, oHold)
}

// A window with the pointer but no gesture object gets gesture commands
// without the server failing or the client being disconnected.
func TestPointerGesturesWithoutObjects(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, _, _ := surfaceMapper(t, c, events)()
	commands <- ports.PointerFocus{ID: w.ID, X: 1, Y: 1}
	commands <- ports.GestureBeginTo{ID: w.ID, Kind: ports.GesturePinch, Fingers: 2}
	commands <- ports.GestureUpdateTo{ID: w.ID, Kind: ports.GesturePinch, Scale: 2}
	commands <- ports.GestureEndTo{ID: w.ID, Kind: ports.GesturePinch}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}
