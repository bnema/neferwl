package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/extidlenotify"
	"github.com/bnema/purego-libwayland/protocol/idleinhibit"
	"github.com/bnema/purego-libwayland/protocol/wlroutputpower"
	"github.com/bnema/wlturbo"
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
	c.Context().Register(p)
	return id, p
}

// next dispatches until p gets an event, or fails after wait.
func (p *eventProxy) next(t *testing.T, c *wlturbo.Display, wait time.Duration) [2]uint32 {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-p.events:
			return e
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatal("no event")
	return [2]uint32{}
}

// none checks p gets no event for d.
func (p *eventProxy) none(t *testing.T, c *wlturbo.Display, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-p.events:
			t.Fatalf("unexpected event %v", e)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A notification idles after its timeout, resumes on user activity and
// idles again; an idle inhibitor holds it, an input-idle one ignores it.
func TestIdleNotification(t *testing.T) {
	s, events, commands, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	notifier := bindVersion(t, c, "ext_idle_notifier_v1", 2)
	registerProtocol(t, c, notifier)
	id, n := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetIdleNotification, id, uint32(50), seat)
	idled, resumed := uint32(extidlenotify.ExtIdleNotificationV1EventIdled), uint32(extidlenotify.ExtIdleNotificationV1EventResumed)
	if e := n.next(t, c, 2*time.Second); e[0] != idled {
		t.Fatalf("event %v, want idled", e)
	}
	commands <- ports.UserActivity{}
	if e := n.next(t, c, 2*time.Second); e[0] != resumed {
		t.Fatalf("event %v, want resumed", e)
	}
	if e := n.next(t, c, 2*time.Second); e[0] != idled {
		t.Fatalf("event %v, want idled again", e)
	}
	commands <- ports.UserActivity{}
	n.next(t, c, 2*time.Second) // resumed

	// An idle inhibitor holds it; input-idle notifications still fire.
	_, surf, _ := surfaceMapper(t, c, events)()
	inhibitMgr := bindProtocol(t, c, "zwp_idle_inhibit_manager_v1")
	in := c.AllocateID()
	registerProtocol(t, c, in)
	requestProtocol(t, c, inhibitMgr, idleinhibit.ZwpIdleInhibitManagerV1RequestCreateInhibitor, in, surf)
	clientEvent[ports.IdleInhibit](t, events)
	inputID, input := newEventProxy(c)
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetInputIdleNotification, inputID, uint32(50), seat)
	if e := input.next(t, c, 2*time.Second); e[0] != idled {
		t.Fatalf("input-idle event %v, want idled", e)
	}
	n.none(t, c, 200*time.Millisecond)
	// The inhibitor goes: the held notification counts from zero.
	requestProtocol(t, c, in, idleinhibit.ZwpIdleInhibitorV1RequestDestroy)
	if e := n.next(t, c, 2*time.Second); e[0] != idled {
		t.Fatalf("event %v, want idled after the inhibitor", e)
	}
	requestProtocol(t, c, id, extidlenotify.ExtIdleNotificationV1RequestDestroy)
	requestProtocol(t, c, inputID, extidlenotify.ExtIdleNotificationV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
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
	requestProtocol(t, c, mgr, wlroutputpower.ZwlrOutputPowerManagerV1RequestGetOutputPower, id, output)
	modeEv, failed := uint32(wlroutputpower.ZwlrOutputPowerV1EventMode), uint32(wlroutputpower.ZwlrOutputPowerV1EventFailed)
	on, off := uint32(wlroutputpower.ZwlrOutputPowerV1ModeOn), uint32(wlroutputpower.ZwlrOutputPowerV1ModeOff)
	if e := p.next(t, c, 2*time.Second); e != [2]uint32{modeEv, on} {
		t.Fatalf("initial %v", e)
	}
	name := testOutputs[0].Info.Name
	requestProtocol(t, c, id, wlroutputpower.ZwlrOutputPowerV1RequestSetMode, off)
	if v := clientEvent[ports.OutputPower](t, events); v != (ports.OutputPower{Output: name, On: false}) {
		t.Fatal(v)
	}
	commands <- ports.SetOutputs{Outputs: testOutputs, Off: []string{name}}
	if e := p.next(t, c, 2*time.Second); e != [2]uint32{modeEv, off} {
		t.Fatalf("after off %v", e)
	}
	// Unchanged state: no event.
	commands <- ports.SetOutputs{Outputs: testOutputs, Off: []string{name}}
	p.none(t, c, 50*time.Millisecond)
	commands <- ports.SetOutputs{Outputs: testOutputs}
	if e := p.next(t, c, 2*time.Second); e != [2]uint32{modeEv, on} {
		t.Fatalf("after on %v", e)
	}
	// The output goes: the object fails.
	commands <- ports.SetOutputs{}
	if e := p.next(t, c, 2*time.Second); e[0] != failed {
		t.Fatalf("after unplug %v", e)
	}
	// Back with the same name: the failed object controls nothing.
	commands <- ports.SetOutputs{Outputs: testOutputs}
	requestProtocol(t, c, id, wlroutputpower.ZwlrOutputPowerV1RequestSetMode, off)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if v, ok := ev.(ports.OutputPower); ok {
			t.Fatalf("failed object sent %+v", v)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// A timeout shorter than the activity interval still waits for it, so
// continuous input keeps the notification from idling.
func TestIdleShortTimeout(t *testing.T) {
	s, _, commands, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	notifier := bindVersion(t, c, "ext_idle_notifier_v1", 2)
	id, n := newEventProxy(c)
	start := time.Now()
	requestProtocol(t, c, notifier, extidlenotify.ExtIdleNotifierV1RequestGetIdleNotification, id, uint32(0), seat)
	n.next(t, c, 2*time.Second)
	if d := time.Since(start); d < ports.ActivityInterval {
		t.Fatalf("idled after %v, before the activity interval", d)
	}
	commands <- ports.UserActivity{}
	n.next(t, c, 2*time.Second) // resumed
	for range 4 {
		time.Sleep(ports.ActivityInterval / 2)
		commands <- ports.UserActivity{}
	}
	n.none(t, c, ports.ActivityInterval/2)
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
	requestProtocol(t, c, mgr, wlroutputpower.ZwlrOutputPowerManagerV1RequestGetOutputPower, id, output)
	requestProtocol(t, c, id, wlroutputpower.ZwlrOutputPowerV1RequestSetMode, uint32(7))
	expectProtocolError(t, c, id, uint32(wlroutputpower.ZwlrOutputPowerV1ErrorInvalidMode))
}
