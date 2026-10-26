package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/extidlenotify"
	"github.com/bnema/go-wayland-bindings/server/idleinhibit"
	"github.com/bnema/go-wayland-bindings/server/wlroutputpowermanagement"
	"github.com/bnema/neferwl/internal/adapters/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"github.com/stretchr/testify/mock"
)

// eventProxy records the opcodes, and first uint argument, of its events.
type eventProxy struct {
	wlturbo.BaseProxy
	events chan [2]uint32
}

func (p *eventProxy) Dispatch(e *wlturbo.Event) {
	var arg uint32
	if len(e.Data()) >= 4 {
		arg = e.Uint32()
	}
	p.events <- [2]uint32{uint32(e.Opcode), arg}
}

func newEventProxy(c *wlturbo.Display) (uint32, *eventProxy) {
	id := c.AllocateID()
	p := &eventProxy{events: make(chan [2]uint32, 16)}
	p.SetID(id)
	registerWireProxy(c, p)
	return id, p
}

// next dispatches until p gets an event.
func (p *eventProxy) next(t *testing.T, c *wlturbo.Display) [2]uint32 {
	t.Helper()
	return recvWire(t, c, "idle event", p.events)
}

// none checks p got no event for what the display processed so far: a
// roundtrip orders everything the server already sent before the check.
func (p *eventProxy) none(t *testing.T, c *wlturbo.Display) {
	t.Helper()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-p.events:
		t.Fatalf("unexpected event %v", e)
	default:
	}
}

// barrier returns once the display goroutine ran everything queued so far.
func barrier(t *testing.T, s *Server) {
	t.Helper()
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
}

// noArmed fails if an idle timer was started; call it after a barrier.
func noArmed(t *testing.T, timers <-chan armed) {
	t.Helper()
	select {
	case a := <-timers:
		t.Fatalf("unexpected idle timer %v", a.d)
	default:
	}
}

// A notification idles after its timeout, resumes on user activity and
// idles again; an idle inhibitor holds it, an input-idle one ignores it.
func TestIdleNotification(t *testing.T) {
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Events: events, Commands: commands})
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	notifier := bindVersion(t, c, "ext_idle_notifier_v1", 2)
	registerProtocol(t, c, notifier)
	id, n := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetIdleNotification, id, uint32(50), seat)
	idled, resumed := uint32(extidlenotify.ExtIdleNotificationV1EventIdled), uint32(extidlenotify.ExtIdleNotificationV1EventResumed)
	nextArmed(t, timers).fire()
	if e := n.next(t, c); e[0] != idled {
		t.Fatalf("event %v, want idled", e)
	}
	commands <- ports.UserActivity{}
	if e := n.next(t, c); e[0] != resumed {
		t.Fatalf("event %v, want resumed", e)
	}
	nextArmed(t, timers).fire()
	if e := n.next(t, c); e[0] != idled {
		t.Fatalf("event %v, want idled again", e)
	}
	commands <- ports.UserActivity{}
	n.next(t, c) // resumed
	running := nextArmed(t, timers)

	// An idle inhibitor holds it; input-idle notifications still fire.
	_, surf, _ := surfaceMapper(t, c, events)()
	inhibitMgr := bindProtocol(t, c, "zwp_idle_inhibit_manager_v1")
	in := c.AllocateID()
	registerProtocol(t, c, in)
	requestProtocol(t, c, inhibitMgr, idleinhibit.ZwpIdleInhibitManagerV1RequestCreateInhibitor, in, surf)
	clientEvent[ports.IdleInhibit](t, events)
	barrier(t, s)
	noArmed(t, timers)
	inputID, input := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetInputIdleNotification, inputID, uint32(50), seat)
	nextArmed(t, timers).fire()
	if e := input.next(t, c); e[0] != idled {
		t.Fatalf("input-idle event %v, want idled", e)
	}
	// The timer that ran before the inhibitor must not idle the held one.
	running.fire()
	n.none(t, c)
	// The inhibitor goes: the held notification counts from zero.
	requestProtocol(t, c, in, idleinhibit.ZwpIdleInhibitorV1RequestDestroy)
	nextArmed(t, timers).fire()
	if e := n.next(t, c); e[0] != idled {
		t.Fatalf("event %v, want idled after the inhibitor", e)
	}
	requestProtocol(t, c, id, extidlenotify.ExtIdleNotificationV1RequestDestroy)
	requestProtocol(t, c, inputID, extidlenotify.ExtIdleNotificationV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// A D-Bus idle inhibition (IdleInhibited) holds notifications like an
// inhibitor surface: a running timer stops, input-idle ones still fire,
// and held ones count from zero once it ends. D-Bus activity
// (IdleActivity) resumes them like input.
func TestBusIdleInhibition(t *testing.T) {
	inhibited := make(chan bool)
	activity := make(chan struct{})
	s, dir, timers := idleClockServerWith(t, Channels{IdleInhibited: inhibited, IdleActivity: activity})
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	notifier := bindVersion(t, c, "ext_idle_notifier_v1", 2)
	registerProtocol(t, c, notifier)
	idled, resumed := uint32(extidlenotify.ExtIdleNotificationV1EventIdled), uint32(extidlenotify.ExtIdleNotificationV1EventResumed)

	// D-Bus activity resumes an idle notification and restarts it.
	id, n := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetIdleNotification, id, uint32(300), seat)
	nextArmed(t, timers).fire()
	if e := n.next(t, c); e[0] != idled {
		t.Fatalf("event %v, want idled", e)
	}
	activity <- struct{}{}
	if e := n.next(t, c); e[0] != resumed {
		t.Fatalf("event %v, want resumed", e)
	}
	running := nextArmed(t, timers)

	// The inhibition stops the running timer and arms none. The forwarder
	// takes the second send only once the first is applied.
	inhibited <- true
	inhibited <- true
	barrier(t, s)
	noArmed(t, timers)
	running.fire()
	n.none(t, c)
	inputID, input := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetInputIdleNotification, inputID, uint32(50), seat)
	nextArmed(t, timers).fire()
	if e := input.next(t, c); e[0] != idled {
		t.Fatalf("input-idle event %v, want idled", e)
	}
	n.none(t, c)
	inhibited <- false
	nextArmed(t, timers).fire()
	if e := n.next(t, c); e[0] != idled {
		t.Fatalf("event %v, want idled after the inhibition", e)
	}
}

// An output power object reports the mode, turns set_mode into a core
// event, hears core's decision, and fails when its output goes.
func TestOutputPower(t *testing.T) {
	s, events, commands, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	output := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, output)
	mgr := bindProtocol(t, c, "zwlr_output_power_manager_v1")
	registerProtocol(t, c, mgr)
	id, p := newEventProxy(c)
	requestProtocol(t, c, mgr, wlroutputpowermanagement.ZwlrOutputPowerManagerV1RequestGetOutputPower, id, output)
	modeEv, failed := uint32(wlroutputpowermanagement.ZwlrOutputPowerV1EventMode), uint32(wlroutputpowermanagement.ZwlrOutputPowerV1EventFailed)
	on, off := uint32(wlroutputpowermanagement.ZwlrOutputPowerV1ModeOn), uint32(wlroutputpowermanagement.ZwlrOutputPowerV1ModeOff)
	if e := p.next(t, c); e != [2]uint32{modeEv, on} {
		t.Fatalf("initial %v", e)
	}
	name := testOutputs[0].Info.Name
	requestProtocol(t, c, id, wlroutputpowermanagement.ZwlrOutputPowerV1RequestSetMode, off)
	if v := clientEvent[ports.OutputPower](t, events); v != (ports.OutputPower{Output: name, On: false}) {
		t.Fatal(v)
	}
	commands <- ports.SetOutputs{Outputs: testOutputs, Off: []string{name}}
	if e := p.next(t, c); e != [2]uint32{modeEv, off} {
		t.Fatalf("after off %v", e)
	}
	// Unchanged state: no event.
	commands <- ports.SetOutputs{Outputs: testOutputs, Off: []string{name}}
	flushCommands(t, s, commands)
	p.none(t, c)
	commands <- ports.SetOutputs{Outputs: testOutputs}
	if e := p.next(t, c); e != [2]uint32{modeEv, on} {
		t.Fatalf("after on %v", e)
	}
	// The output goes: the object fails.
	commands <- ports.SetOutputs{}
	if e := p.next(t, c); e[0] != failed {
		t.Fatalf("after unplug %v", e)
	}
	// Back with the same name: the failed object controls nothing.
	commands <- ports.SetOutputs{Outputs: testOutputs}
	flushCommands(t, s, commands)
	requestProtocol(t, c, id, wlroutputpowermanagement.ZwlrOutputPowerV1RequestSetMode, off)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if v, ok := ev.(ports.OutputPower); ok {
			t.Fatalf("failed object sent %+v", v)
		}
	default:
	}
}

// armed is one idle timer started through the clock.
type armed struct {
	d    time.Duration
	fire func()
}

// idleClockServer runs a server whose idle timers fire only when the test
// calls them; every started timer is sent on the returned channel.
func idleClockServer(t *testing.T) (*Server, chan ports.ClientCommand, string, chan armed) {
	t.Helper()
	commands := make(chan ports.ClientCommand, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Commands: commands})
	return s, commands, dir, timers
}

// idleClockServerWith is idleClockServer with the given channels.
func idleClockServerWith(t *testing.T, ch Channels) (*Server, string, chan armed) {
	t.Helper()
	dir := t.TempDir()
	timers := make(chan armed, 16)
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().AfterFunc(mock.Anything, mock.Anything).RunAndReturn(func(d time.Duration, f func()) ports.Timer {
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().Stop().Return(true).Maybe()
		timers <- armed{d, f}
		return timer
	}).Maybe()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Clock: clock}, ch, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() { cancel(); <-done })
	return s, dir, timers
}

//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func nextArmed(t *testing.T, timers <-chan armed) armed {
	t.Helper()
	select {
	case a := <-timers:
		return a
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("no idle timer started")
		return armed{}
	}
}

// A timeout shorter than the activity interval still waits for it, so
// continuous input keeps the notification from idling; a timer replaced
// by user activity is stale when it fires.
func TestIdleShortTimeout(t *testing.T) {
	s, commands, dir, timers := idleClockServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	notifier := bindVersion(t, c, "ext_idle_notifier_v1", 2)
	id, n := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetIdleNotification, id, uint32(0), seat)
	first := nextArmed(t, timers)
	if first.d != ports.ActivityInterval {
		t.Fatalf("timer %v, want the activity interval", first.d)
	}
	first.fire()
	if e := n.next(t, c); e[0] != uint32(extidlenotify.ExtIdleNotificationV1EventIdled) {
		t.Fatalf("event %v, want idled", e)
	}
	commands <- ports.UserActivity{}
	if e := n.next(t, c); e[0] != uint32(extidlenotify.ExtIdleNotificationV1EventResumed) {
		t.Fatalf("event %v, want resumed", e)
	}
	stale := nextArmed(t, timers)
	commands <- ports.UserActivity{}
	current := nextArmed(t, timers)
	stale.fire()
	n.none(t, c)
	current.fire()
	if e := n.next(t, c); e[0] != uint32(extidlenotify.ExtIdleNotificationV1EventIdled) {
		t.Fatalf("event %v, want idled", e)
	}
}

// An unknown mode is a protocol error.
func TestOutputPowerInvalidMode(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	output := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, output)
	mgr := bindProtocol(t, c, "zwlr_output_power_manager_v1")
	id := c.AllocateID()
	registerProtocol(t, c, id)
	requestProtocol(t, c, mgr, wlroutputpowermanagement.ZwlrOutputPowerManagerV1RequestGetOutputPower, id, output)
	requestProtocol(t, c, id, wlroutputpowermanagement.ZwlrOutputPowerV1RequestSetMode, uint32(7))
	expectProtocolError(t, c, id, uint32(wlroutputpowermanagement.ZwlrOutputPowerV1ErrorInvalidMode))
}
