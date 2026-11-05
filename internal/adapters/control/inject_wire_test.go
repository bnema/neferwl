package control

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

//testpolicy:realtime real Unix control socket exchanges and shutdown
func TestInjectProtocol(t *testing.T) {
	km := injectKeymap(t)
	security := portsmocks.NewMockSessionSecurity(t)
	state := domain.SecurityState{Generation: 7}
	security.EXPECT().Snapshot().Return(state)
	updates := make(chan *xkb.Keymap, 1)
	path := filepath.Join(t.TempDir(), "control.sock")
	s, err := Listen(path, workspaceid.WithPrefix("inject"), Options{Keymap: km, Keymaps: updates, Security: security})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan ports.ControlRequest, 1)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	connect := func() net.Conn {
		t.Helper()
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		return c
	}
	conn := connect()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	for _, req := range []map[string]any{
		{"kind": "key", "combo": "bad-key"}, {"kind": "unknown"}, {"kind": "key", "combo": "a", "wait": "bad"}, {"kind": "swipe", "fingers": 3, "dir": "bad"},
	} {
		req["id"], req["op"] = 1, "inject"
		if err := enc.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response reply
		if err := dec.Decode(&response); err != nil || response.Error != ports.ControlErrBadRequest {
			t.Fatal(response, err)
		}
	}
	exchange := func(conn net.Conn, req map[string]any, announcement bool) []ports.InputEvent {
		t.Helper()
		req["id"], req["op"] = 2, "inject"
		if err := json.NewEncoder(conn).Encode(req); err != nil {
			t.Fatal(err)
		}
		var message ports.ControlInject
		select {
		case raw := <-out:
			var ok bool
			message, ok = raw.(ports.ControlInject)
			if !ok {
				t.Fatalf("request %T", raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing injection")
		}
		if message.Caps != domain.CapsAll || cap(message.Reply) != 1 {
			t.Fatal(message)
		}
		events := message.Events
		for _, raw := range events {
			if ev, ok := raw.(ports.SecurityInput); !ok || ev.State != state {
				t.Fatal(raw)
			}
		}
		if announcement {
			if ev := events[0].(ports.SecurityInput).Event; ev != (ports.InputDeviceAdded{ID: controlDeviceID, Name: "control"}) {
				t.Fatal(ev)
			}
			events = events[1:]
		} else {
			if _, ok := events[0].(ports.SecurityInput).Event.(ports.InputDeviceAdded); ok {
				t.Fatal("duplicate announcement")
			}
		}
		if req["wait"] != nil {
			if message.Wait != domain.WaitPresented || message.Until == nil || message.Until.AppID != "foot" || message.Timeout != 2*time.Second {
				t.Fatal(message)
			}
		} else if message.Wait != domain.DefaultWait || message.Until != nil || message.Timeout != 10*time.Second {
			t.Fatal(message)
		}
		message.Reply <- ports.ControlReply{Done: domain.WaitCommitted}
		var response reply
		if err := json.NewDecoder(conn).Decode(&response); err != nil || !response.OK {
			t.Fatal(response, err)
		}
		return events
	}
	events := exchange(conn, map[string]any{"kind": "press", "combo": "Super", "wait": "presented", "until": "focused app_id=foot", "timeout": "2s"}, true)
	if len(events) != 1 || !events[0].(ports.SecurityInput).Event.(ports.KeyEvent).Pressed {
		t.Fatal(events)
	}
	// A newly received map takes over held-key tracking, quarantining release.
	next := injectKeymap(t)
	updates <- next
	events = exchange(conn, map[string]any{"kind": "key", "combo": "q"}, false)
	for _, raw := range events {
		if ev := raw.(ports.SecurityInput).Event.(ports.KeyEvent); ev.Mods&domain.ModSuper != 0 {
			t.Fatal(ev)
		}
	}
	// Other kinds share the same request port and first-inject bookkeeping.
	for _, req := range []map[string]any{
		{"kind": "type", "text": "Hi!"}, {"kind": "pointer", "cmd": "click"}, {"kind": "swipe", "fingers": 4, "dir": "up"}, {"kind": "release", "combo": "a"},
	} {
		exchange(conn, req, false)
	}
	other := connect()
	exchange(other, map[string]any{"kind": "key", "combo": "Return"}, true)
	// Both clients remain open: shutdown must interrupt their blocked scanners.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open-client shutdown hung")
	}
}
