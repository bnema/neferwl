package wayland

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/inputtimestamps"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// stampRecorder is the ordered log shared by the proxies of one client, so a
// test sees whether a timestamp comes right before its event.
type stampRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *stampRecorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *stampRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

// wait round-trips until the log holds n lines.
func (r *stampRecorder) wait(t *testing.T, c *wlturbo.Display, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if got := r.snapshot(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("log has %v, want %d lines", r.snapshot(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stampedKeyboard logs the key events of one wl_keyboard under its tag.
type stampedKeyboard struct {
	wlturbo.BaseProxy
	tag string
	log *stampRecorder
}

func (p *stampedKeyboard) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.KeyboardEventKey) {
		_ = e.Uint32() // serial
		p.log.add("%s key %d", p.tag, e.Uint32())
	}
}

// stampedPointer logs the motion, button and axis events of a wl_pointer.
type stampedPointer struct {
	wlturbo.BaseProxy
	log *stampRecorder
}

func (p *stampedPointer) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.PointerEventMotion):
		p.log.add("motion %d", e.Uint32())
	case uint16(wayland.PointerEventButton):
		_ = e.Uint32() // serial
		p.log.add("button %d", e.Uint32())
	case uint16(wayland.PointerEventAxis):
		p.log.add("axis %d", e.Uint32())
	case uint16(wayland.PointerEventAxisStop):
		p.log.add("axis_stop %d", e.Uint32())
	}
}

// stampProxy logs zwp_input_timestamps_v1.timestamp events.
type stampProxy struct {
	wlturbo.BaseProxy
	log *stampRecorder
}

func (p *stampProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(inputtimestamps.ZwpInputTimestampsV1EventTimestamp) {
		hi, lo, ns := e.Uint32(), e.Uint32(), e.Uint32()
		p.log.add("ts %d %d %d", hi, lo, ns)
	}
}

func stampSubscription(t *testing.T, c *wlturbo.Display, log *stampRecorder, manager uint32, request uint32, device uint32) uint32 {
	t.Helper()
	id := c.AllocateID()
	p := &stampProxy{log: log}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, manager, request, id, device)
	return id
}

func TestInputTimestampsGlobal(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	g, ok := c.Registry().FindGlobal("zwp_input_timestamps_manager_v1")
	if !ok || g.Version != 1 {
		t.Fatalf("zwp_input_timestamps_manager_v1: %+v, %v", g, ok)
	}
}

func TestInputTimestampsKeyboard(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	log := &stampRecorder{}
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	var keyboards [2]uint32
	for i, tag := range []string{"subscribed", "plain"} {
		keyboards[i] = c.AllocateID()
		kp := &stampedKeyboard{tag: tag, log: log}
		kp.SetID(keyboards[i])
		registerWireProxy(c, kp)
		requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, keyboards[i])
	}
	manager := bindProtocol(t, c, "zwp_input_timestamps_manager_v1")
	ts := stampSubscription(t, c, log, manager, inputtimestamps.ZwpInputTimestampsManagerV1RequestGetKeyboardTimestamps, keyboards[0])
	w := toplevelMapper(t, c, events)()
	commands <- ports.FocusWindow{ID: w.ID}

	commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: 30, Pressed: true, Time: 5*time.Second + 123456789}}
	got := log.wait(t, c, 3)
	want := []string{"ts 0 5 123456789", "subscribed key 5123", "plain key 5123"}
	if !slices.Equal(got, want) {
		t.Fatalf("log %v, want %v", got, want)
	}

	// Seconds past 32 bits go to sec_hi.
	big := time.Duration(1<<32+7) * time.Second
	commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: 30, Time: big}}
	ms := uint64(big / time.Millisecond)
	got = log.wait(t, c, 6)
	want = append(want, "ts 1 7 0", fmt.Sprint("subscribed key ", uint32(ms)), fmt.Sprint("plain key ", uint32(ms)))
	if !slices.Equal(got, want) {
		t.Fatalf("log %v, want %v", got, want)
	}

	// A destroyed timestamps object sends nothing more.
	requestProtocol(t, c, ts, inputtimestamps.ZwpInputTimestampsV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	commands <- ports.ForwardKey{ID: w.ID, Key: ports.KeyEvent{Keycode: 31, Pressed: true, Time: 9 * time.Second}}
	got = log.wait(t, c, 8)
	want = append(want, "subscribed key 9000", "plain key 9000")
	if !slices.Equal(got, want) {
		t.Fatalf("log %v, want %v", got, want)
	}
}

func TestInputTimestampsPointer(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	log := &stampRecorder{}
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	pp := &stampedPointer{log: log}
	pp.SetID(pointer)
	registerWireProxy(c, pp)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	manager := bindProtocol(t, c, "zwp_input_timestamps_manager_v1")
	ts := stampSubscription(t, c, log, manager, inputtimestamps.ZwpInputTimestampsManagerV1RequestGetPointerTimestamps, pointer)
	w := toplevelMapper(t, c, events)()
	commands <- ports.PointerFocus{ID: w.ID, X: 0, Y: 0}

	commands <- ports.PointerMotionTo{ID: w.ID, X: 0, Y: 0, Time: 2*time.Second + time.Millisecond}
	commands <- ports.PointerButtonTo{ID: w.ID, Button: 0x110, Pressed: true, Time: 3 * time.Second}
	commands <- ports.PointerAxisTo{ID: w.ID, Axis: ports.PointerAxis{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Set: true, Value: 5}, Time: 4 * time.Second}}
	commands <- ports.PointerAxisTo{ID: w.ID, Axis: ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Stop: true}, Time: 5 * time.Second}}
	got := log.wait(t, c, 8)
	want := []string{
		"ts 0 2 1000000", "motion 2001",
		"ts 0 3 0", "button 3000",
		"ts 0 4 0", "axis 4000",
		"ts 0 5 0", "axis_stop 5000",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("log %v, want %v", got, want)
	}

	// The stamped input path allocates nothing, with a live subscriber.
	var allocs float64
	s.display.Do(func() {
		allocs = testing.AllocsPerRun(100, func() {
			s.applyInput(ports.PointerMotionTo{ID: w.ID, Time: 6 * time.Second})
		})
	})
	if allocs != 0 {
		t.Fatalf("stamped motion allocates %v", allocs)
	}

	// Releasing the pointer makes the object inert and drops its entry;
	// destroying it afterwards is still fine.
	requestProtocol(t, c, pointer, wayland.PointerRequestRelease)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := stampEntries(s); n != 0 {
		t.Fatalf("%d stamp entries after the pointer died", n)
	}
	requestProtocol(t, c, ts, inputtimestamps.ZwpInputTimestampsV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// Destroying the timestamps object before its device drops the entry too.
func TestInputTimestampsDestroyBeforeDevice(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	log := &stampRecorder{}
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	manager := bindProtocol(t, c, "zwp_input_timestamps_manager_v1")
	ts := stampSubscription(t, c, log, manager, inputtimestamps.ZwpInputTimestampsManagerV1RequestGetPointerTimestamps, pointer)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := stampEntries(s); n != 1 {
		t.Fatalf("%d stamp entries, want 1", n)
	}
	requestProtocol(t, c, ts, inputtimestamps.ZwpInputTimestampsV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := stampEntries(s); n != 0 {
		t.Fatalf("%d stamp entries after destroy", n)
	}
}

// stampEntries counts the devices with subscriptions, on the display goroutine.
func stampEntries(s *Server) int {
	var n int
	s.display.Do(func() { n = len(s.seat.keyStamps) + len(s.seat.pointerStamps) })
	return n
}
