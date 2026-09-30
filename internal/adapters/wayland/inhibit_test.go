package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/idleinhibit"
	"github.com/bnema/purego-libwayland/protocol/keyboardshortcutsinhibit"
	"github.com/bnema/wlturbo"
)

type inhibitorProxy struct {
	wlturbo.BaseProxy
	states chan bool
}

func (p *inhibitorProxy) Dispatch(e *wlturbo.Event) {
	p.states <- uint32(e.Opcode) == keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitorV1EventActive
}

// clientEvent waits for the next client event of type T.
func clientEvent[T ports.ClientEvent](t *testing.T, events <-chan ports.ClientEvent) T {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T", zero)
			return zero
		}
	}
}

func TestShortcutsInhibitor(t *testing.T) {
	s, events, commands, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	mgr := bindProtocol(t, c, "zwp_keyboard_shortcuts_inhibit_manager_v1")
	in := c.AllocateID()
	p := &inhibitorProxy{states: make(chan bool, 4)}
	p.SetID(in)
	registerWireProxy(c, p)
	requestProtocol(t, c, mgr, keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1RequestInhibitShortcuts, in, surf, seat)
	if v := clientEvent[ports.ShortcutsInhibit](t, events); v != (ports.ShortcutsInhibit{Window: w.ID, Active: true}) {
		t.Fatal(v)
	}
	// Core decides: active, then inactive.
	state := func(active bool) {
		t.Helper()
		commands <- ports.ShortcutsInhibitState{Window: w.ID, Active: active}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-p.states:
				if got != active {
					t.Fatalf("state %v", got)
				}
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		t.Fatal("no active/inactive event")
	}
	state(true)
	state(false)
	// Destroying it tells core.
	requestProtocol(t, c, in, keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitorV1RequestDestroy)
	if v := clientEvent[ports.ShortcutsInhibit](t, events); v.Active {
		t.Fatal(v)
	}
	// A second inhibitor on the same surface is a protocol error.
	requestProtocol(t, c, mgr, keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1RequestInhibitShortcuts, c.AllocateID(), surf, seat)
	requestProtocol(t, c, mgr, keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1RequestInhibitShortcuts, c.AllocateID(), surf, seat)
	expectProtocolError(t, c, mgr, uint32(keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1ErrorAlreadyInhibited))
}

// Idle inhibitors: several per surface, the window is reported once and
// released when the last one goes.
func TestIdleInhibitors(t *testing.T) {
	s, events, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	mgr := bindProtocol(t, c, "zwp_idle_inhibit_manager_v1")
	a, b := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, a)
	registerProtocol(t, c, b)
	requestProtocol(t, c, mgr, idleinhibit.ZwpIdleInhibitManagerV1RequestCreateInhibitor, a, surf)
	requestProtocol(t, c, mgr, idleinhibit.ZwpIdleInhibitManagerV1RequestCreateInhibitor, b, surf)
	if v := clientEvent[ports.IdleInhibit](t, events); v != (ports.IdleInhibit{Window: w.ID, Active: true}) {
		t.Fatal(v)
	}
	requestProtocol(t, c, a, idleinhibit.ZwpIdleInhibitorV1RequestDestroy)
	requestProtocol(t, c, b, idleinhibit.ZwpIdleInhibitorV1RequestDestroy)
	if v := clientEvent[ports.IdleInhibit](t, events); v.Active {
		t.Fatal(v)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if _, ok := ev.(ports.IdleInhibit); ok {
			t.Fatalf("extra %+v", ev)
		}
	default:
	}
}
