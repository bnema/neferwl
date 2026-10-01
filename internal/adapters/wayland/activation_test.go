package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/xdgactivation"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// tokenProxy receives an xdg_activation_token_v1.done token.
type tokenProxy struct {
	wlturbo.BaseProxy
	tokens chan string
}

func (p *tokenProxy) Dispatch(e *wlturbo.Event) { p.tokens <- e.String() }

// newToken commits a token request, with a serial and seat when given,
// and returns the token.
func newToken(t *testing.T, c *wlturbo.Display, manager uint32, serialSeat ...uint32) string {
	t.Helper()
	id := c.AllocateID()
	p := &tokenProxy{tokens: make(chan string, 1)}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestGetActivationToken, id)
	if len(serialSeat) == 2 {
		requestProtocol(t, c, id, xdgactivation.ActivationTokenV1RequestSetSerial, serialSeat[0], serialSeat[1])
	}
	requestProtocol(t, c, id, xdgactivation.ActivationTokenV1RequestCommit)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case tok := <-p.tokens:
			return tok
		default:
		}
	}
	t.Fatal("no token")
	return ""
}

func activated(t *testing.T, events <-chan ports.ClientEvent, want ports.WindowID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if a, ok := ev.(ports.WindowActivate); ok {
				if a.ID != want {
					t.Fatalf("activated %d, want %d", a.ID, want)
				}
				return
			}
		case <-deadline:
			t.Fatal("no activation")
		}
	}
}

func noActivation(t *testing.T, c *wlturbo.Display, events <-chan ports.ClientEvent) {
	t.Helper()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if _, ok := ev.(ports.WindowActivate); ok {
				t.Fatal("activated with an invalid token")
			}
		case <-deadline:
			return
		}
	}
}

func TestActivationNeedsFocusedRequester(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	mapWindow := surfaceMapper(t, c, events)
	first, _, _ := mapWindow()
	second, surf, _ := mapWindow()
	manager := bindProtocol(t, c, "xdg_activation_v1")

	// Without focus the token is inert.
	token := newToken(t, c, manager)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events)

	// The focused client's token activates, once.
	commands <- ports.FocusWindow{ID: first.ID}
	waitFocus(t, s, first.ID)
	token = newToken(t, c, manager)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	activated(t, events, second.ID)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events)

	// Unknown tokens are ignored.
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, "not-a-token", surf)
	noActivation(t, c, events)
}

// Another client's token is inert while one client has the focus; the
// focused client may hand its token to activate another client's window.
func TestActivationAcrossClients(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	a := protocolClient(t, s, dir)
	winA, _, _ := surfaceMapper(t, a, events)()
	b := protocolClient(t, s, dir)
	winB, surfB, _ := surfaceMapper(t, b, events)()
	managerA := bindProtocol(t, a, "xdg_activation_v1")
	managerB := bindProtocol(t, b, "xdg_activation_v1")
	commands <- ports.FocusWindow{ID: winA.ID}
	waitFocus(t, s, winA.ID)
	token := newToken(t, b, managerB)
	requestProtocol(t, b, managerB, xdgactivation.ActivationV1RequestActivate, token, surfB)
	noActivation(t, b, events)
	token = newToken(t, a, managerA)
	requestProtocol(t, b, managerB, xdgactivation.ActivationV1RequestActivate, token, surfB)
	activated(t, events, winB.ID)

	// A token dies when the focus leaves its client.
	token = newToken(t, a, managerA)
	commands <- ports.FocusWindow{ID: winB.ID}
	waitFocus(t, s, winB.ID)
	requestProtocol(t, b, managerB, xdgactivation.ActivationV1RequestActivate, token, surfB)
	noActivation(t, b, events)
}

// A press serial earns a token only while the press is the latest user
// action: after a focus change it is stale.
func TestActivationPressSerial(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "xdg_activation_v1")
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	press := func() uint32 {
		var serial uint32
		done := make(chan struct{})
		s.display.Do(func() {
			s.serial++
			s.seat.press, s.seat.pressClient, s.seat.pressAt = s.serial, s.windows[w.ID].xdg.resource.Client(), time.Now()
			serial = s.serial
			close(done)
		})
		<-done
		return serial
	}
	serial := press()
	token := newToken(t, c, manager, serial, seat)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	activated(t, events, w.ID)
	// A wrong serial earns nothing.
	token = newToken(t, c, manager, serial+100, seat)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events)
	// The focus moved after the press: stale.
	commands <- ports.FocusWindow{ID: w.ID}
	commands <- ports.FocusWindow{}
	waitFocus(t, s, 0)
	token = newToken(t, c, manager, serial, seat)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events)
}

func TestActivationTokenCommittedTwice(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	manager := bindProtocol(t, c, "xdg_activation_v1")
	id := c.AllocateID()
	registerProtocol(t, c, id)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestGetActivationToken, id)
	requestProtocol(t, c, id, xdgactivation.ActivationTokenV1RequestCommit)
	requestProtocol(t, c, id, xdgactivation.ActivationTokenV1RequestCommit)
	expectProtocolError(t, c, id, uint32(xdgactivation.ActivationTokenV1ErrorAlreadyUsed))
}

// waitFocus waits until the display goroutine has applied a focus change.
func waitFocus(t *testing.T, s *Server, id ports.WindowID) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		done := make(chan ports.WindowID, 1)
		s.display.Do(func() { done <- s.seat.focused })
		if <-done == id {
			return
		}
	}
	t.Fatal("focus not applied", id)
}
