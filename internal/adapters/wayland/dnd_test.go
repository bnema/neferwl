package wayland

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

const (
	actCopy = uint32(wayland.DataDeviceManagerDndActionCopy)
	actMove = uint32(wayland.DataDeviceManagerDndActionMove)
	actAsk  = uint32(wayland.DataDeviceManagerDndActionAsk)
)

// dndEvent is one data device, offer or source event a test client saw.
type dndEvent struct {
	name string
	mime string
	x, y float64
	n    uint32
}

type dndDeviceEvents struct {
	wlturbo.BaseProxy
	c   *wlturbo.Display
	log chan dndEvent
	// offer is the id of the latest data_offer.
	offer atomic.Uint32
}

func (p *dndDeviceEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.DataDeviceEventDataOffer):
		o := &dndOfferEvents{log: p.log}
		o.SetID(e.Uint32())
		p.offer.Store(o.ID())
		registerWireProxy(p.c, o)
	case uint16(wayland.DataDeviceEventEnter):
		serial, _ := e.Uint32(), e.Uint32()
		x, y := e.Fixed().Float64(), e.Fixed().Float64()
		p.log <- dndEvent{name: "enter", x: x, y: y, n: e.Uint32() + serial*0}
	case uint16(wayland.DataDeviceEventLeave):
		p.log <- dndEvent{name: "leave"}
	case uint16(wayland.DataDeviceEventMotion):
		_ = e.Uint32()
		p.log <- dndEvent{name: "motion", x: e.Fixed().Float64(), y: e.Fixed().Float64()}
	case uint16(wayland.DataDeviceEventDrop):
		p.log <- dndEvent{name: "drop"}
	}
}

type dndOfferEvents struct {
	wlturbo.BaseProxy
	log chan dndEvent
}

func (p *dndOfferEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.DataOfferEventOffer):
		p.log <- dndEvent{name: "offer", mime: e.String()}
	case uint16(wayland.DataOfferEventSourceActions):
		p.log <- dndEvent{name: "source_actions", n: e.Uint32()}
	case uint16(wayland.DataOfferEventAction):
		p.log <- dndEvent{name: "offer_action", n: e.Uint32()}
	}
}

type dndSourceEvents struct {
	wlturbo.BaseProxy
	log chan dndEvent
}

func (p *dndSourceEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.DataSourceEventTarget):
		p.log <- dndEvent{name: "target", mime: e.String()}
	case uint16(wayland.DataSourceEventSend):
		mime := e.String()
		fd := int(e.Fd())
		_, _ = unix.Write(fd, []byte("dnd data"))
		_ = unix.Close(fd)
		p.log <- dndEvent{name: "send", mime: mime}
	case uint16(wayland.DataSourceEventCancelled):
		p.log <- dndEvent{name: "cancelled"}
	case uint16(wayland.DataSourceEventDndDropPerformed):
		p.log <- dndEvent{name: "drop_performed"}
	case uint16(wayland.DataSourceEventDndFinished):
		p.log <- dndEvent{name: "finished"}
	case uint16(wayland.DataSourceEventAction):
		p.log <- dndEvent{name: "action", n: e.Uint32()}
	}
}

// dndClient is a client with a toplevel, a pointer and a data device.
type dndClient struct {
	keys    chan uint32
	c       *wlturbo.Display
	manager uint32
	device  *dndDeviceEvents
	serials chan uint32
	comp    uint32
	shm     uint32
	surf    uint32
	window  ports.WindowID
	log     chan dndEvent
}

// keySerials records the serial of each key event.
type keySerials struct {
	wlturbo.BaseProxy
	serials chan uint32
}

func (p *keySerials) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.KeyboardEventKey) {
		p.serials <- e.Uint32()
	}
}

func newDndClient(t *testing.T, s *Server, dir string, events <-chan ports.ClientEvent) *dndClient {
	t.Helper()
	return newDndClientAt(t, s, dir, events, 3)
}

// newDndClientAt is newDndClient with wl_data_device_manager at version.
func newDndClientAt(t *testing.T, s *Server, dir string, events <-chan ports.ClientEvent, version uint32) *dndClient {
	t.Helper()
	c := protocolClient(t, s, dir)
	d := &dndClient{c: c, log: make(chan dndEvent, 64), serials: make(chan uint32, 8), keys: make(chan uint32, 8)}
	kb := &keySerials{serials: d.keys}
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	ptr := &pressSerials{serials: d.serials}
	ptr.SetID(c.AllocateID())
	registerWireProxy(c, ptr)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, ptr.ID())
	kb.SetID(c.AllocateID())
	registerWireProxy(c, kb)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, kb.ID())
	d.manager = bindVersion(t, c, "wl_data_device_manager", version)
	d.device = &dndDeviceEvents{c: c, log: d.log}
	d.device.SetID(c.AllocateID())
	registerWireProxy(c, d.device)
	requestProtocol(t, c, d.manager, wayland.DataDeviceManagerRequestGetDataDevice, d.device.ID(), seat)
	d.comp = bindVersion(t, c, "wl_compositor", 5)
	d.shm = bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, d.shm)
	w, surf, _ := surfaceMapperAt(t, c, events, 5)()
	d.window, d.surf = w.ID, surf
	return d
}

// lastOffer is the id of the latest offer the client got.
func (d *dndClient) lastOffer(t *testing.T) uint32 {
	t.Helper()
	id := d.device.offer.Load()
	if id == 0 {
		t.Fatal("no offer")
	}
	return id
}

// next returns the next event the client sees.
func (d *dndClient) next(t *testing.T) dndEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if err := d.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-d.log:
			return ev
		case <-deadline:
			t.Fatal("no event")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// expect waits for the next event and checks its name.
func (d *dndClient) expect(t *testing.T, name string) dndEvent {
	t.Helper()
	ev := d.next(t)
	if ev.name != name {
		t.Fatalf("event %+v, want %s", ev, name)
	}
	return ev
}

// none checks that no event arrives.
func (d *dndClient) none(t *testing.T, commands chan<- ports.ClientCommand) {
	t.Helper()
	// Observing a key sent through the command queue proves earlier
	// compositor commands ran. The roundtrip then drains wire events.
	d.key(t, commands)
	if err := d.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-d.log:
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

func (d *dndClient) source(t *testing.T, actions uint32, mimes ...string) uint32 {
	t.Helper()
	src := &dndSourceEvents{log: d.log}
	src.SetID(d.c.AllocateID())
	registerWireProxy(d.c, src)
	requestProtocol(t, d.c, d.manager, wayland.DataDeviceManagerRequestCreateDataSource, src.ID())
	for _, m := range mimes {
		requestProtocol(t, d.c, src.ID(), wayland.DataSourceRequestOffer, m)
	}
	if actions != 0 {
		requestProtocol(t, d.c, src.ID(), wayland.DataSourceRequestSetActions, actions)
	}
	return src.ID()
}

// press makes the client's window press a button and returns the serial.
func (d *dndClient) press(t *testing.T, commands chan<- ports.ClientCommand) uint32 {
	t.Helper()
	commands <- ports.PointerFocus{ID: d.window, X: 1, Y: 1}
	commands <- ports.PointerButtonTo{ID: d.window, Button: 0x110, Pressed: true, Grab: true, Time: time.Millisecond}
	deadline := time.After(2 * time.Second)
	for {
		if err := d.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case serial := <-d.serials:
			return serial
		case <-deadline:
			t.Fatal("no button")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// press2 returns the next pointer button serial.
func (d *dndClient) press2(t *testing.T) uint32 {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if err := d.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case serial := <-d.serials:
			return serial
		case <-deadline:
			t.Fatal("no button")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// release releases the button press took and waits for the client to see it.
func (d *dndClient) release(t *testing.T, commands chan<- ports.ClientCommand) {
	t.Helper()
	commands <- ports.PointerButtonTo{ID: d.window, Button: 0x110, Time: 2 * time.Millisecond}
	d.press2(t)
}

// key presses a key on the client's focused window and returns the serial.
func (d *dndClient) key(t *testing.T, commands chan<- ports.ClientCommand) uint32 {
	t.Helper()
	commands <- ports.FocusWindow{ID: d.window}
	commands <- ports.ForwardKey{ID: d.window, Key: ports.KeyEvent{Keycode: 30, Pressed: true, Time: 3 * time.Millisecond}}
	deadline := time.After(2 * time.Second)
	for {
		if err := d.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case serial := <-d.keys:
			return serial
		case <-deadline:
			t.Fatal("no key")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// events returns what the client saw up to and including name.
func (d *dndClient) until(t *testing.T, name string) []dndEvent {
	t.Helper()
	var got []dndEvent
	for {
		ev := d.next(t)
		got = append(got, ev)
		if ev.name == name {
			return got
		}
	}
}

func (d *dndClient) startDrag(t *testing.T, src, icon, serial uint32) {
	t.Helper()
	d.startDragFrom(t, src, d.surf, icon, serial)
}

// startDragFrom is startDrag with an origin surface.
func (d *dndClient) startDragFrom(t *testing.T, src, origin, icon, serial uint32) {
	t.Helper()
	requestProtocol(t, d.c, d.device.ID(), wayland.DataDeviceRequestStartDrag, src, origin, icon, serial)
	if err := d.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// buffer makes a w×h ARGB shm buffer.
func (d *dndClient) buffer(t *testing.T, w, h int) uint32 {
	t.Helper()
	fd, err := unix.MemfdCreate("dnd-icon", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(w*h*4)); err != nil {
		t.Fatal(err)
	}
	pool, buf := d.c.AllocateID(), d.c.AllocateID()
	registerProtocol(t, d.c, pool)
	registerProtocol(t, d.c, buf)
	if err := wireRequest(d.c, d.shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(w*h*4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, d.c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(w), int32(h), int32(w*4), uint32(0))
	return buf
}

func (d *dndClient) surfaceNew(t *testing.T) uint32 {
	t.Helper()
	id := d.c.AllocateID()
	requestProtocol(t, d.c, d.comp, wayland.CompositorRequestCreateSurface, id)
	registerProtocol(t, d.c, id)
	return id
}

// dndNextEvent returns the next client event of type T, skipping others.
func dndNextEvent[T ports.ClientEvent](t *testing.T, events <-chan ports.ClientEvent) T {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-deadline:
			t.Fatalf("no %T event", *new(T))
		}
	}
}

// dndNoEvent checks that no client event of type T is pending.
func dndNoEvent[T ports.ClientEvent](t *testing.T, s *Server, events <-chan ports.ClientEvent) {
	t.Helper()
	// Callers have observed completion of the triggering request/command.
	// Put a marker behind its emitted events, including the forwarding queue.
	marker := ports.WindowResized{ID: ^ports.WindowID(0)}
	if !s.display.Do(func() { s.emit(marker) }) {
		t.Fatal("display stopped")
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(ports.WindowResized); ok && v.ID == marker.ID {
				return
			}
			if _, ok := ev.(T); ok {
				t.Fatalf("unexpected %T: %+v", ev, ev)
			}
		case <-deadline:
			t.Fatal("no event barrier")
		}
	}
}

// dndPair is two clients with a drag started from a on a source.
type dndPair struct {
	s        *Server
	events   chan ports.ClientEvent
	commands chan ports.ClientCommand
	a, b     *dndClient
}

func newDndPair(t *testing.T) *dndPair {
	t.Helper()
	s, events, commands, dir := keyboardServer(t)
	p := &dndPair{s: s, events: events, commands: commands}
	p.a = newDndClient(t, s, dir, events)
	p.b = newDndClient(t, s, dir, events)
	return p
}

// start begins a drag of a source offering text/plain, copy and move.
func (p *dndPair) start(t *testing.T, icon uint32) uint32 {
	t.Helper()
	src := p.a.source(t, actCopy|actMove, "text/plain", "text/uri-list")
	p.a.startDrag(t, src, icon, p.a.press(t, p.commands))
	return src
}

// enter makes b the drag target and returns the offer id.
func (p *dndPair) enter(t *testing.T) uint32 {
	t.Helper()
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 5, Y: 6, Time: 10 * time.Millisecond}
	for _, want := range []dndEvent{{name: "offer", mime: "text/plain"}, {name: "offer", mime: "text/uri-list"}, {name: "source_actions", n: actCopy | actMove}} {
		if ev := p.b.next(t); ev != want {
			t.Fatalf("event %+v, want %+v", ev, want)
		}
	}
	ev := p.b.expect(t, "enter")
	if ev.x != 5 || ev.y != 6 || ev.n == 0 {
		t.Fatalf("enter %+v", ev)
	}
	return ev.n
}

// A1: a drag from a to b delivers enter, motion and drop with the offer;
// the action is negotiated; the data reaches b; a is told every step.
func TestDataDragBetweenClients(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	if v := dndNextEvent[ports.DataDragStarted](t, p.events); v.Icon != 0 {
		t.Fatalf("started %+v", v)
	}
	offer := p.enter(t)

	p.commands <- ports.DataDragMotion{ID: p.b.window, X: 7, Y: 8, Time: 20 * time.Millisecond}
	if ev := p.b.expect(t, "motion"); ev.x != 7 || ev.y != 8 {
		t.Fatalf("motion %+v", ev)
	}

	// b accepts and prefers move: both sides learn the action.
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy|actMove, actMove)
	if ev := p.a.expect(t, "target"); ev.mime != "text/plain" {
		t.Fatalf("target %+v", ev)
	}
	if ev := p.a.expect(t, "action"); ev.n != actMove {
		t.Fatalf("action %+v", ev)
	}
	if ev := p.b.expect(t, "offer_action"); ev.n != actMove {
		t.Fatalf("offer action %+v", ev)
	}

	// Finishing before the drop is a protocol error, so only after it.
	p.commands <- ports.DataDragDrop{}
	p.b.expect(t, "drop")
	p.b.expect(t, "leave")
	p.a.expect(t, "drop_performed")

	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	if err := wireRequest(p.b.c, offer, uint16(wayland.DataOfferRequestReceive), []int{fds[1]}, "text/plain"); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fds[1])
	if err := p.b.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if ev := p.a.expect(t, "send"); ev.mime != "text/plain" {
		t.Fatalf("send %+v", ev)
	}
	buf := make([]byte, 16)
	n, err := unix.Read(fds[0], buf)
	for errors.Is(err, unix.EINTR) {
		n, err = unix.Read(fds[0], buf)
	}
	if err != nil || string(buf[:n]) != "dnd data" {
		t.Fatalf("received %q, %v", buf[:max(n, 0)], err)
	}

	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestFinish)
	p.a.expect(t, "finished")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestDestroy)
	p.a.none(t, p.commands)
	p.b.none(t, p.commands)
	// core ended the drag: wayland does not echo it.
	dndNoEvent[ports.DataDragEnded](t, p.s, p.events)
}

// The action follows wlroots: the preferred one if both allow it, else the
// first of copy, move, ask they share, else none.
func TestChooseAction(t *testing.T) {
	for _, tc := range []struct{ avail, preferred, want uint32 }{
		{actCopy | actMove, actMove, actMove},
		{actCopy | actMove, actAsk, actCopy},
		{actMove | actAsk, 0, actMove},
		{actAsk, 0, actAsk},
		{0, actCopy, 0},
	} {
		if got := chooseAction(tc.avail, tc.preferred); got != tc.want {
			t.Errorf("chooseAction(%d, %d) = %d, want %d", tc.avail, tc.preferred, got, tc.want)
		}
	}
}

// A target and a source sharing no action leave the drop refused.
func TestDataDragNoCommonAction(t *testing.T) {
	p := newDndPair(t)
	src := p.a.source(t, actCopy, "text/plain")
	p.a.startDrag(t, src, 0, p.a.press(t, p.commands))
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 1, Y: 1}
	p.b.expect(t, "offer")
	p.b.expect(t, "source_actions")
	offer := p.b.expect(t, "enter").n
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actMove, actMove)
	p.a.expect(t, "target")
	p.commands <- ports.DataDragDrop{}
	p.b.expect(t, "leave")
	// The refused target no longer accepts.
	if ev := p.a.expect(t, "target"); ev.mime != "" {
		t.Fatalf("target %+v", ev)
	}
	p.a.expect(t, "cancelled")
}

// A2: a release over nothing, or over a target that accepted nothing,
// cancels the source; the target gets leave and no drop.
func TestDataDragRefusedDrop(t *testing.T) {
	t.Run("nothing", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		p.commands <- ports.DataDragDrop{}
		p.a.expect(t, "cancelled")
		p.b.none(t, p.commands)
	})
	t.Run("not accepted", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		p.enter(t)
		p.commands <- ports.DataDragDrop{}
		p.b.expect(t, "leave")
		p.a.expect(t, "cancelled")
		p.b.none(t, p.commands)
	})
	t.Run("escape", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		p.enter(t)
		p.commands <- ports.DataDragCancel{}
		p.b.expect(t, "leave")
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragEnded](t, p.s, p.events)
	})
}

// Moving to another surface leaves the first and enters the second; a
// surface of nobody (ID 0) leaves.
func TestDataDragLeavesOnFocusChange(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	p.enter(t)
	p.commands <- ports.DataDragFocus{}
	p.b.expect(t, "leave")
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 2, Y: 3}
	for range 3 {
		p.b.next(t)
	}
	if ev := p.b.expect(t, "enter"); ev.x != 2 || ev.y != 3 {
		t.Fatalf("enter %+v", ev)
	}
}

// A4: the source dying mid-drag ends it: the target gets leave and core a
// DataDragEnded; later commands change nothing.
func TestDataDragSourceDestroyed(t *testing.T) {
	p := newDndPair(t)
	src := p.start(t, 0)
	p.enter(t)
	requestProtocol(t, p.a.c, src, wayland.DataSourceRequestDestroy)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	p.b.expect(t, "leave")
	dndNextEvent[ports.DataDragEnded](t, p.events)
	p.commands <- ports.DataDragMotion{ID: p.b.window, X: 9, Y: 9}
	p.commands <- ports.DataDragDrop{}
	p.b.none(t, p.commands)
}

// A drag whose origin client dies ends the same way.
func TestDataDragOriginDies(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	p.enter(t)
	_ = p.a.c.Close()
	p.b.expect(t, "leave")
	dndNextEvent[ports.DataDragEnded](t, p.events)
}

// A6: a start_drag without the current press of the origin client is
// refused: the source is cancelled and core hears nothing.
func TestDataDragInvalidStart(t *testing.T) {
	t.Run("stale serial", func(t *testing.T) {
		p := newDndPair(t)
		serial := p.a.press(t, p.commands)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial-1)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("other client's press", func(t *testing.T) {
		p := newDndPair(t)
		p.a.press(t, p.commands)
		serial := p.b.press(t, p.commands)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("key serial", func(t *testing.T) {
		p := newDndPair(t)
		p.a.press(t, p.commands)
		p.a.release(t, p.commands)
		serial := p.a.key(t, p.commands)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("serial after the release", func(t *testing.T) {
		p := newDndPair(t)
		serial := p.a.press(t, p.commands)
		p.a.release(t, p.commands)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("other surface of the client", func(t *testing.T) {
		p := newDndPair(t)
		serial := p.a.press(t, p.commands)
		other := p.a.surfaceNew(t)
		p.a.startDragFrom(t, p.a.source(t, actCopy, "text/plain"), other, 0, serial)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("press of the first button", func(t *testing.T) {
		// A second button does not make a new grab: only the first serial.
		p := newDndPair(t)
		first := p.a.press(t, p.commands)
		p.commands <- ports.PointerButtonTo{ID: p.a.window, Button: 0x111, Pressed: true, Time: 2 * time.Millisecond}
		second := p.a.press2(t)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, second)
		p.a.expect(t, "cancelled")
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, first)
		// Core is told the grab's button: its release drops.
		if ev := dndNextEvent[ports.DataDragStarted](t, p.events); ev.Button != 0x110 {
			t.Fatalf("drag button %#x, want the grab's", ev.Button)
		}
	})
	t.Run("grab release consumed by core", func(t *testing.T) {
		// A window drag consumes the release: the next grab press
		// still makes the serial a drag can name.
		p := newDndPair(t)
		stale := p.a.press(t, p.commands)
		serial := p.a.press(t, p.commands)
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, stale)
		p.a.expect(t, "cancelled")
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial)
		dndNextEvent[ports.DataDragStarted](t, p.events)
	})
	t.Run("grab release of a gone window", func(t *testing.T) {
		p := newDndPair(t)
		serial := p.a.press(t, p.commands)
		p.commands <- ports.PointerButtonTo{ID: 9999, Button: 0x110, Time: 2 * time.Millisecond}
		p.a.key(t, p.commands) // the release is applied once the key is seen
		p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), 0, serial)
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("used source", func(t *testing.T) {
		p := newDndPair(t)
		src := p.start(t, 0)
		p.commands <- ports.DataDragCancel{}
		p.a.expect(t, "cancelled")
		p.a.startDrag(t, src, 0, p.a.press(t, p.commands))
		p.a.expect(t, "cancelled")
	})
	t.Run("drag in progress", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		dndNextEvent[ports.DataDragStarted](t, p.events)
		other := p.a.source(t, actCopy, "text/plain")
		p.a.startDrag(t, other, 0, p.a.press(t, p.commands))
		p.a.expect(t, "cancelled")
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
	t.Run("icon with another role", func(t *testing.T) {
		p := newDndPair(t)
		serial := p.a.press(t, p.commands)
		src := p.a.source(t, actCopy, "text/plain")
		// The origin toplevel is a role already.
		requestProtocol(t, p.a.c, p.a.device.ID(), wayland.DataDeviceRequestStartDrag, src, p.a.surf, p.a.surf, serial)
		expectProtocolError(t, p.a.c, p.a.device.ID(), uint32(wayland.DataDeviceErrorRole))
		dndNoEvent[ports.DataDragStarted](t, p.s, p.events)
	})
}

// A nil source is a drag internal to the origin client: only its own
// surfaces see it, without offer.
func TestDataDragWithoutSource(t *testing.T) {
	p := newDndPair(t)
	p.a.startDrag(t, 0, 0, p.a.press(t, p.commands))
	dndNextEvent[ports.DataDragStarted](t, p.events)
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 1, Y: 1}
	p.b.none(t, p.commands)
	p.commands <- ports.DataDragFocus{ID: p.a.window, X: 4, Y: 5}
	if ev := p.a.expect(t, "enter"); ev.n != 0 || ev.x != 4 || ev.y != 5 {
		t.Fatalf("enter %+v", ev)
	}
	p.commands <- ports.DataDragDrop{}
	p.a.expect(t, "drop")
}

// Offer and source requests are validated.
func TestDataDragProtocolErrors(t *testing.T) {
	t.Run("finish before drop", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		offer := p.enter(t)
		requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestFinish)
		expectProtocolError(t, p.b.c, offer, uint32(wayland.DataOfferErrorInvalidFinish))
	})
	t.Run("offer action mask", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		offer := p.enter(t)
		requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, uint32(8), uint32(0))
		expectProtocolError(t, p.b.c, offer, uint32(wayland.DataOfferErrorInvalidActionMask))
	})
	t.Run("offer preferred action", func(t *testing.T) {
		p := newDndPair(t)
		p.start(t, 0)
		offer := p.enter(t)
		requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy, actMove)
		expectProtocolError(t, p.b.c, offer, uint32(wayland.DataOfferErrorInvalidAction))
	})
	t.Run("source action mask", func(t *testing.T) {
		p := newDndPair(t)
		src := p.a.source(t, 0, "text/plain")
		requestProtocol(t, p.a.c, src, wayland.DataSourceRequestSetActions, uint32(16))
		expectProtocolError(t, p.a.c, src, uint32(wayland.DataSourceErrorInvalidActionMask))
	})
	t.Run("selection with actions", func(t *testing.T) {
		p := newDndPair(t)
		src := p.a.source(t, actCopy, "text/plain")
		requestProtocol(t, p.a.c, p.a.device.ID(), wayland.DataDeviceRequestSetSelection, src, uint32(0))
		expectProtocolError(t, p.a.c, src, uint32(wayland.DataSourceErrorInvalidSource))
	})
	t.Run("actions after the drag started", func(t *testing.T) {
		p := newDndPair(t)
		src := p.start(t, 0)
		requestProtocol(t, p.a.c, src, wayland.DataSourceRequestSetActions, actCopy)
		expectProtocolError(t, p.a.c, src, uint32(wayland.DataSourceErrorInvalidSource))
	})
}

// A destroyed offer before finish cancels the source.
func TestDataDragOfferDestroyedBeforeFinish(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	offer := p.enter(t)
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy, actCopy)
	p.a.expect(t, "target")
	p.a.expect(t, "action")
	p.commands <- ports.DataDragDrop{}
	p.b.expect(t, "offer_action")
	p.b.expect(t, "drop")
	p.b.expect(t, "leave")
	p.a.expect(t, "drop_performed")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestDestroy)
	p.a.expect(t, "cancelled")
}

// iconContent reports whether the server still holds content for id.
func iconContent(t *testing.T, s *Server, id ports.WindowID) bool {
	t.Helper()
	var ok bool
	if !s.display.Do(func() { _, ok = s.latest[id] }) {
		t.Fatal("display stopped")
	}
	return ok
}

// The icon is a surface with its own window ID: its size and the
// offsets of its commits reach core, its content is published, and both go
// when the drag ends.
func TestDataDragIcon(t *testing.T) {
	p := newDndPair(t)
	icon := p.a.surfaceNew(t)
	buf := p.a.buffer(t, 8, 6)
	// An offset on an ordinary surface is not used.
	requestProtocol(t, p.a.c, p.a.surf, wayland.SurfaceRequestOffset, int32(5), int32(5))
	requestProtocol(t, p.a.c, p.a.surf, wayland.SurfaceRequestCommit)

	src := p.a.source(t, actCopy, "text/plain")
	p.a.startDrag(t, src, icon, p.a.press(t, p.commands))
	started := dndNextEvent[ports.DataDragStarted](t, p.events)
	if started.Icon == 0 {
		t.Fatal("no icon ID")
	}
	id := started.Icon
	if iconContent(t, p.s, id) {
		t.Fatal("content before the first commit")
	}

	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestOffset, int32(-3), int32(-4))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id, X: -3, Y: -4, W: 8, H: 6}) {
		t.Fatalf("icon %+v", got)
	}
	if !iconContent(t, p.s, id) {
		t.Fatal("no icon content")
	}

	// Offsets add up from commit to commit.
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestOffset, int32(-1), int32(2))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id, X: -4, Y: -2, W: 8, H: 6}) {
		t.Fatalf("icon %+v", got)
	}
	// The icon never takes input.
	dndNoEvent[ports.InputRegionChanged](t, p.s, p.events)

	p.commands <- ports.DataDragCancel{}
	p.a.expect(t, "cancelled")
	if iconContent(t, p.s, id) {
		t.Fatal("icon content kept after the drag")
	}
}

// An icon committed before start_drag shows at once.
func TestDataDragIconCommittedEarly(t *testing.T) {
	p := newDndPair(t)
	icon := p.a.surfaceNew(t)
	buf := p.a.buffer(t, 4, 4)
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), icon, p.a.press(t, p.commands))
	id := dndNextEvent[ports.DataDragStarted](t, p.events).Icon
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id, W: 4, H: 4}) {
		t.Fatalf("icon %+v", got)
	}
	if !iconContent(t, p.s, id) {
		t.Fatal("no icon content")
	}
}

// Before wl_surface v5 the icon offset rides on attach and adds up.
func TestDataDragIconAttachOffset(t *testing.T) {
	p := newDndPair(t)
	comp4 := bindVersion(t, p.a.c, "wl_compositor", 4)
	icon := p.a.c.AllocateID()
	requestProtocol(t, p.a.c, comp4, wayland.CompositorRequestCreateSurface, icon)
	registerProtocol(t, p.a.c, icon)
	buf := p.a.buffer(t, 4, 4)
	p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), icon, p.a.press(t, p.commands))
	id := dndNextEvent[ports.DataDragStarted](t, p.events).Icon
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(-2), int32(-1))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id, X: -2, Y: -1, W: 4, H: 4}) {
		t.Fatalf("icon %+v", got)
	}
}

// Destroying the icon mid-drag hides it; the drag goes on.
func TestDataDragIconDestroyed(t *testing.T) {
	p := newDndPair(t)
	icon := p.a.surfaceNew(t)
	buf := p.a.buffer(t, 8, 6)
	p.a.startDrag(t, p.a.source(t, actCopy|actMove, "text/plain", "text/uri-list"), icon, p.a.press(t, p.commands))
	id := dndNextEvent[ports.DataDragStarted](t, p.events).Icon
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got.W != 8 {
		t.Fatalf("icon %+v", got)
	}
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestDestroy)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id}) {
		t.Fatalf("icon %+v", got)
	}
	if iconContent(t, p.s, id) {
		t.Fatal("icon content kept")
	}
	p.enter(t)
}

// lockedPair is a pair of clients on a server that can lock the session.
func lockedPair(t *testing.T) (*dndPair, string) {
	t.Helper()
	gate := &sessionsecurity.Gate{}
	events := make(chan ports.ClientEvent, 128)
	commands := make(chan ports.ClientCommand, 32)
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Security: gate, Keymap: keymapText(t)}, Channels{Events: events, Commands: commands, SecurityChanges: make(chan ports.SecurityState, 16), SecurityEvents: make(chan ports.SecurityBackendEvent, 16)}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	p := &dndPair{s: s, events: events, commands: commands}
	p.a = newDndClient(t, s, dir, events)
	p.b = newDndClient(t, s, dir, events)
	return p, dir
}

func lockSession(t *testing.T, s *Server, dir string) {
	t.Helper()
	lc := protocolClient(t, s, dir)
	if _, err := bindLockManager(t, lc).Lock(); err != nil {
		t.Fatal(err)
	}
	if err := lc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// A7: the session lock cancels a drag at once: target leave, source
// cancelled, icon gone.
func TestDataDragLockCancels(t *testing.T) {
	p, dir := lockedPair(t)
	icon := p.a.surfaceNew(t)
	buf := p.a.buffer(t, 8, 6)
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	src := p.a.source(t, actCopy|actMove, "text/plain", "text/uri-list")
	p.a.startDrag(t, src, icon, p.a.press(t, p.commands))
	id := dndNextEvent[ports.DataDragStarted](t, p.events).Icon
	p.enter(t)

	lockSession(t, p.s, dir)
	p.b.expect(t, "leave")
	p.a.expect(t, "cancelled")
	if iconContent(t, p.s, id) {
		t.Fatal("icon content kept")
	}
	// A new drag is refused while protected.
	src2 := p.a.source(t, actCopy, "text/plain")
	p.a.startDrag(t, src2, 0, uint32(1))
	p.a.expect(t, "cancelled")
}

// GO-006: a dropped transfer waiting for its finish dies with the lock, for
// a v3 and a pre-v3 target alike.
func TestDataDropLockCancelsPending(t *testing.T) {
	for _, version := range []uint32{3, 2} {
		p, dir := lockedPair(t)
		p.b = newDndClientAt(t, p.s, dir, p.events, version)
		p.start(t, 0)
		p.commands <- ports.DataDragFocus{ID: p.b.window, X: 5, Y: 6}
		p.b.until(t, "enter")
		offer := p.b.lastOffer(t)
		requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
		if version >= 3 {
			requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy, actCopy)
		}
		p.a.until(t, "target")
		p.commands <- ports.DataDragDrop{}
		p.a.until(t, "drop_performed")
		lockSession(t, p.s, dir)
		p.a.expect(t, "cancelled")
	}
}

// A subsurface of the focused tree is a valid origin.
func TestDataDragFromSubsurface(t *testing.T) {
	p := newDndPair(t)
	serial := p.a.press(t, p.commands)
	child := p.a.surfaceNew(t)
	subc := bindProtocol(t, p.a.c, "wl_subcompositor")
	requestProtocol(t, p.a.c, subc, wayland.SubcompositorRequestGetSubsurface, p.a.c.AllocateID(), child, p.a.surf)
	p.a.startDragFrom(t, p.a.source(t, actCopy, "text/plain"), child, 0, serial)
	dndNextEvent[ports.DataDragStarted](t, p.events)
}

// GO-003: destroying the offers of the target withdraws the acceptance: the
// release then cancels, with no drop.
func TestDataDragOfferDestroyedBeforeRelease(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	offer := p.enter(t)
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy, actCopy)
	p.a.expect(t, "target")
	p.a.expect(t, "action")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestDestroy)
	if err := p.b.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if ev := p.a.expect(t, "target"); ev.mime != "" {
		t.Fatalf("target %+v", ev)
	}
	if ev := p.a.expect(t, "action"); ev.n != 0 {
		t.Fatalf("action %+v", ev)
	}
	p.commands <- ports.DataDragDrop{}
	got := p.b.until(t, "leave")
	for _, ev := range got {
		if ev.name == "drop" {
			t.Fatalf("drop after the offer was destroyed: %+v", got)
		}
	}
	p.a.expect(t, "cancelled")
}

// GO-004: with ask, the target picks the final action after the drop; the
// source hears it just before dnd_finished.
func TestDataDragAsk(t *testing.T) {
	p := newDndPair(t)
	src := p.a.source(t, actCopy|actMove|actAsk, "text/plain")
	p.a.startDrag(t, src, 0, p.a.press(t, p.commands))
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 1, Y: 1}
	p.b.until(t, "enter")
	id := p.b.lastOffer(t)
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestSetActions, actCopy|actMove|actAsk, actAsk)
	p.a.expect(t, "target")
	if ev := p.a.expect(t, "action"); ev.n != actAsk {
		t.Fatalf("action %+v", ev)
	}
	p.commands <- ports.DataDragDrop{}
	p.b.until(t, "leave")
	p.a.expect(t, "drop_performed")

	// Finishing while the action is ask is an error; the choice comes first.
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestAccept, uint32(2), "text/plain")
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestSetActions, actCopy|actMove, actMove)
	p.a.expect(t, "target")
	p.b.none(t, p.commands) // no offer.action after the drop
	p.a.none(t, p.commands) // the final action waits for the finish
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestFinish)
	if ev := p.a.expect(t, "action"); ev.n != actMove {
		t.Fatalf("action %+v", ev)
	}
	p.a.expect(t, "finished")
}

// A final preferred action outside the source's is an error.
func TestDataDragAskFinalActionNotInSource(t *testing.T) {
	p := newDndPair(t)
	src := p.a.source(t, actCopy|actAsk, "text/plain")
	p.a.startDrag(t, src, 0, p.a.press(t, p.commands))
	p.commands <- ports.DataDragFocus{ID: p.b.window, X: 1, Y: 1}
	p.b.until(t, "enter")
	id := p.b.lastOffer(t)
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestSetActions, actCopy|actMove|actAsk, actAsk)
	p.a.expect(t, "target")
	p.a.expect(t, "action")
	p.commands <- ports.DataDragDrop{}
	p.b.until(t, "leave")
	requestProtocol(t, p.b.c, id, wayland.DataOfferRequestSetActions, actMove, actMove)
	expectProtocolError(t, p.b.c, id, uint32(wayland.DataOfferErrorInvalidAction))
}

// GO-005: a pre-v3 target copies: a source that does not allow copy is
// cancelled, one that does gets it.
func TestDataDragLegacyTarget(t *testing.T) {
	setup := func(t *testing.T, actions uint32) (*dndPair, uint32) {
		s, events, commands, dir := keyboardServer(t)
		p := &dndPair{s: s, events: events, commands: commands}
		p.a = newDndClient(t, s, dir, events)
		p.b = newDndClientAt(t, s, dir, events, 2)
		src := p.a.source(t, actions, "text/plain")
		p.a.startDrag(t, src, 0, p.a.press(t, p.commands))
		p.commands <- ports.DataDragFocus{ID: p.b.window, X: 1, Y: 1}
		p.b.until(t, "enter")
		id := p.b.lastOffer(t)
		// Legacy accept is feedback only: deliberately never send it.
		return p, id
	}
	t.Run("move only", func(t *testing.T) {
		p, _ := setup(t, actMove)
		p.commands <- ports.DataDragDrop{}
		got := p.b.until(t, "leave")
		for _, ev := range got {
			if ev.name == "drop" {
				t.Fatalf("drop for a source without copy: %+v", got)
			}
		}
		p.a.until(t, "cancelled")
	})
	t.Run("copy", func(t *testing.T) {
		p, id := setup(t, actCopy|actMove)
		p.commands <- ports.DataDragDrop{}
		p.b.expect(t, "drop")
		p.b.expect(t, "leave")
		if ev := p.a.expect(t, "action"); ev.n != actCopy {
			t.Fatalf("action %+v", ev)
		}
		p.a.expect(t, "drop_performed")
		// GO-006: destroying the last offer finishes the transfer.
		requestProtocol(t, p.b.c, id, wayland.DataOfferRequestDestroy)
		p.a.expect(t, "finished")
	})
}

// GO-007: of several offset requests before one commit, the last counts.
func TestDataDragIconOffsetReplacedBeforeCommit(t *testing.T) {
	p := newDndPair(t)
	icon := p.a.surfaceNew(t)
	buf := p.a.buffer(t, 8, 6)
	p.a.startDrag(t, p.a.source(t, actCopy, "text/plain"), icon, p.a.press(t, p.commands))
	id := dndNextEvent[ports.DataDragStarted](t, p.events).Icon
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestOffset, int32(-1), int32(-1))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestOffset, int32(-5), int32(-7))
	requestProtocol(t, p.a.c, icon, wayland.SurfaceRequestCommit)
	if err := p.a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := dndNextEvent[ports.DataDragIcon](t, p.events); got != (ports.DataDragIcon{ID: id, X: -5, Y: -7, W: 8, H: 6}) {
		t.Fatalf("icon %+v", got)
	}
}

func TestDataDragLastTargetDeviceReleased(t *testing.T) {
	p := newDndPair(t)
	p.start(t, 0)
	offer := p.enter(t)
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
	requestProtocol(t, p.b.c, offer, wayland.DataOfferRequestSetActions, actCopy, actCopy)
	p.a.expect(t, "target")
	p.a.expect(t, "action")
	p.b.expect(t, "offer_action")
	requestProtocol(t, p.b.c, p.b.device.ID(), wayland.DataDeviceRequestRelease)
	if err := p.b.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	p.commands <- ports.DataDragDrop{}
	if ev := p.a.expect(t, "target"); ev.mime != "" {
		t.Fatalf("target %+v", ev)
	}
	if ev := p.a.expect(t, "action"); ev.n != 0 {
		t.Fatalf("action %+v", ev)
	}
	p.a.expect(t, "cancelled")
	// The offer was deliberately kept alive; it must not keep a pending drop.
	if !p.s.display.Do(func() {
		if len(p.s.dndDrops) != 0 {
			t.Error("pending drop without a device")
		}
	}) {
		t.Fatal("display stopped")
	}
}

func TestDataDragMultipleOffersShareAction(t *testing.T) {
	for _, name := range []string{"finish", "acceptor destroyed"} {
		t.Run(name, func(t *testing.T) {
			p := newDndPair(t)
			seat := bindVersion(t, p.b.c, "wl_seat", 8)
			second := &dndDeviceEvents{c: p.b.c, log: make(chan dndEvent, 64)}
			second.SetID(p.b.c.AllocateID())
			registerWireProxy(p.b.c, second)
			requestProtocol(t, p.b.c, p.b.manager, wayland.DataDeviceManagerRequestGetDataDevice, second.ID(), seat)
			if err := p.b.c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			p.start(t, 0)
			first := p.enter(t)
			other := &dndClient{c: p.b.c, device: second, log: second.log}
			other.until(t, "enter")
			two := other.lastOffer(t)
			requestProtocol(t, p.b.c, first, wayland.DataOfferRequestAccept, uint32(1), "text/plain")
			requestProtocol(t, p.b.c, first, wayland.DataOfferRequestSetActions, actCopy|actMove, actCopy)
			p.a.expect(t, "target")
			p.a.expect(t, "action")
			for _, d := range []*dndClient{p.b, other} {
				if ev := d.expect(t, "offer_action"); ev.n != actCopy {
					t.Fatalf("copy: %+v", ev)
				}
			}
			requestProtocol(t, p.b.c, two, wayland.DataOfferRequestSetActions, actCopy|actMove, actMove)
			if ev := p.a.expect(t, "action"); ev.n != actMove {
				t.Fatalf("source action %+v", ev)
			}
			for _, d := range []*dndClient{p.b, other} {
				if ev := d.expect(t, "offer_action"); ev.n != actMove {
					t.Fatalf("move: %+v", ev)
				}
			}
			if name == "finish" {
				p.commands <- ports.DataDragDrop{}
				for _, d := range []*dndClient{p.b, other} {
					d.expect(t, "drop")
					d.expect(t, "leave")
				}
				p.a.expect(t, "drop_performed")
				// Either offer finishes the same negotiated move transfer.
				requestProtocol(t, p.b.c, two, wayland.DataOfferRequestFinish)
				p.a.expect(t, "finished")
				return
			}
			requestProtocol(t, p.b.c, first, wayland.DataOfferRequestDestroy)
			if err := p.b.c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if ev := p.a.expect(t, "target"); ev.mime != "" {
				t.Fatalf("stale acceptance %+v", ev)
			}
			if ev := p.a.expect(t, "action"); ev.n != 0 {
				t.Fatalf("stale action %+v", ev)
			}
			if ev := other.expect(t, "offer_action"); ev.n != 0 {
				t.Fatalf("remaining offer %+v", ev)
			}
			p.commands <- ports.DataDragDrop{}
			p.a.expect(t, "cancelled")
			other.expect(t, "leave")
		})
	}
}
