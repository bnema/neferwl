package control

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"testing/synctest"
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
	defer func() { _ = s.Close() }()
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
		t.Cleanup(func() { _ = c.Close() })
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
		message.Admission <- ports.ControlAdmission{}
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

// The device announcement repeats until core accepts a batch: a refused
// (locked) batch never reached the input path.
func TestInjectAnnouncesUntilAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{ids: workspaceid.WithPrefix("inject"), done: make(chan struct{}), injector: newInjector(injectKeymap(t), nil)}
		out := make(chan ports.ControlRequest)
		announced := false
		for _, refuse := range []string{ports.ControlErrLocked, "", ""} {
			go func() {
				r := (<-out).(ports.ControlInject)
				_, first := r.Events[0].(ports.InputDeviceAdded)
				if first == announced {
					t.Errorf("announcement sent=%v after announced=%v", first, announced)
				}
				r.Admission <- ports.ControlAdmission{Err: refuse}
				r.Reply <- ports.ControlReply{Err: refuse}
			}()
			s.dispatch(context.Background(), request{Op: "inject", Kind: "key", Combo: "a"}, out, &announced)
			synctest.Wait()
			if announced != (refuse == "") {
				t.Fatalf("announced=%v after %q", announced, refuse)
			}
		}
	})
}

func TestInjectionAdmissionOrderingAndReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		updates := make(chan *xkb.Keymap, 1)
		s := &Server{ids: workspaceid.WithPrefix("order"), done: make(chan struct{}), injector: newInjector(injectKeymap(t), nil), keymaps: updates}
		out := make(chan ports.ControlRequest, 2)
		firstDone, secondDone := make(chan reply, 1), make(chan reply, 1)
		go func() {
			announced := false
			firstDone <- s.dispatch(t.Context(), request{Op: "inject", Kind: "press", Combo: "Super"}, out, &announced)
		}()
		synctest.Wait()
		first := (<-out).(ports.ControlInject)
		next := injectKeymap(t)
		updates <- next
		if s.injectMu.TryLock() {
			s.injectMu.Unlock()
			t.Fatal("admission serialization released early")
		}
		go func() {
			announced := false
			secondDone <- s.dispatch(t.Context(), request{Op: "inject", Kind: "key", Combo: "q"}, out, &announced)
		}()
		if len(out) != 0 || len(updates) != 1 {
			t.Fatal("second connection translated/reloaded before admission")
		}
		first.Admission <- ports.ControlAdmission{Err: ports.ControlErrBusy}
		synctest.Wait()
		second := (<-out).(ports.ControlInject)
		for _, ev := range second.Events {
			if key, ok := ev.(ports.KeyEvent); ok && key.Mods&domain.ModSuper != 0 {
				t.Fatal("refused Super leaked into q", key)
			}
		}
		// The second request passed admission serialization while first completion
		// is still pending; replacing the map happened only after rollback.
		if len(updates) != 0 {
			t.Fatal("reload not consumed")
		}
		second.Admission <- ports.ControlAdmission{}
		second.Reply <- ports.ControlReply{}
		first.Reply <- ports.ControlReply{Err: ports.ControlErrBusy}
		if r := <-firstDone; r.Error != ports.ControlErrBusy {
			t.Fatal(r)
		}
		if r := <-secondDone; !r.OK {
			t.Fatal(r)
		}
	})
}

func TestInjectionRefusedSuperRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{ids: workspaceid.WithPrefix("busy"), done: make(chan struct{}), injector: newInjector(injectKeymap(t), nil)}
		out := make(chan ports.ControlRequest)
		announced := false
		go func() {
			r := (<-out).(ports.ControlInject)
			r.Admission <- ports.ControlAdmission{Err: ports.ControlErrBusy}
			r.Reply <- ports.ControlReply{Err: ports.ControlErrBusy}
		}()
		if r := s.dispatch(t.Context(), request{Op: "inject", Kind: "press", Combo: "Super"}, out, &announced); r.Error != ports.ControlErrBusy {
			t.Fatal(r)
		}
		go func() {
			r := (<-out).(ports.ControlInject)
			for _, ev := range r.Events {
				if key, ok := ev.(ports.KeyEvent); ok && key.Mods&domain.ModSuper != 0 {
					t.Error("q would trigger Super+q", key)
				}
			}
			r.Admission <- ports.ControlAdmission{}
			r.Reply <- ports.ControlReply{}
		}()
		if r := s.dispatch(t.Context(), request{Op: "inject", Kind: "key", Combo: "q"}, out, &announced); !r.OK {
			t.Fatal(r)
		}
	})
}

func TestInjectionCancellationRollsBack(t *testing.T) {
	for _, sent := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := &Server{ids: workspaceid.WithPrefix("cancel"), done: make(chan struct{}), injector: newInjector(injectKeymap(t), nil)}
			out := make(chan ports.ControlRequest)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				announced := false
				s.dispatch(ctx, request{Op: "inject", Kind: "press", Combo: "Super"}, out, &announced)
			}()
			if sent {
				<-out
			}
			synctest.Wait()
			cancel()
			<-done
			s.injectMu.Lock()
			if ev := s.injector.km.Key(16, true, 0); ev.Mods&domain.ModSuper != 0 {
				t.Fatal("cancelled translation leaked", ev)
			}
			s.injectMu.Unlock()
		})
	}
}

//testpolicy:realtime Two real socket clients verify admission ordering independently of completion.
func TestTwoConnectionsShareAdmittedModifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	s, err := Listen(path, workspaceid.WithPrefix("order"), Options{Keymap: injectKeymap(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out := make(chan ports.ControlRequest)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	connect := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	first, second := connect(), connect()
	if err := json.NewEncoder(first).Encode(request{Op: "inject", Kind: "press", Combo: "Super"}); err != nil {
		t.Fatal(err)
	}
	receive := func() ports.ControlInject {
		t.Helper()
		select {
		case r := <-out:
			return r.(ports.ControlInject)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return ports.ControlInject{}
		}
	}
	a := receive()
	if err := json.NewEncoder(second).Encode(request{Op: "inject", Kind: "key", Combo: "q"}); err != nil {
		t.Fatal(err)
	}
	if s.injectMu.TryLock() {
		s.injectMu.Unlock()
		t.Fatal("serialization released before admission")
	}
	a.Admission <- ports.ControlAdmission{}
	b := receive() // Must arrive without completing a.
	found := false
	for _, ev := range b.Events {
		if key, ok := ev.(ports.KeyEvent); ok && key.Keysym == "q" && key.Pressed {
			found = true
			if key.Mods&domain.ModSuper == 0 {
				t.Fatal("second client lost admitted Super")
			}
		}
	}
	if !found {
		t.Fatal("missing q")
	}
	b.Admission <- ports.ControlAdmission{}
	b.Reply <- ports.ControlReply{}
	a.Reply <- ports.ControlReply{}
	for _, conn := range []net.Conn{first, second} {
		var r reply
		if err := json.NewDecoder(conn).Decode(&r); err != nil || !r.OK {
			t.Fatal(r, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
