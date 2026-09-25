package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/xdgactivation"
	"github.com/bnema/wlturbo"
)

// tokenProxy receives an xdg_activation_token_v1.done token.
type tokenProxy struct {
	wlturbo.BaseProxy
	tokens chan string
}

func (p *tokenProxy) Dispatch(e *wlturbo.Event) { p.tokens <- e.String() }

// newToken commits a token request and returns the token.
func newToken(t *testing.T, c *wlturbo.Display, manager uint32) string {
	t.Helper()
	id := c.AllocateID()
	p := &tokenProxy{tokens: make(chan string, 1)}
	p.SetID(id)
	c.Context().Register(p)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestGetActivationToken, id)
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
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	token = newToken(t, c, manager)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	activated(t, events, second.ID)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events)

	// Unknown tokens are ignored.
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, "not-a-token", surf)
	noActivation(t, c, events)
}
