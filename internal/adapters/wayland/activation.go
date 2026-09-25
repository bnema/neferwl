package wayland

import (
	"crypto/rand"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgactivation"
	"github.com/bnema/purego-libwayland/server"
)

// xdg-activation: a client that has the user's attention (keyboard focus,
// or the last key or button press) gets a token, and the client it hands
// the token to (a link opened in the browser, a notification action) may
// bring its window forward with it. Other tokens are issued but inert, so
// a background app cannot steal the focus.

// tokenLifetime is how long a token can activate a window, and how recent
// a press must be to earn one.
const tokenLifetime = 10 * time.Second

// maxTokens bounds the valid tokens kept; the oldest goes first.
const maxTokens = 32

// activationToken is an issued valid token. Tokens of a client die when
// the focus moves away from it, even on an automatic focus change such as
// a new window mapping: a hand-off racing a transient window then fails
// safe, without stealing focus.
type activationToken struct {
	client server.Client
	at     time.Time
}

func registerActivation(d *server.Display, s *Server) error {
	return xdgactivation.NewActivationV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgactivation.NewActivationV1(c, int32(v), id, activation{s})
	})
}

type activation struct{ server *Server }

func (activation) Destroy(*xdgactivation.ActivationV1) {}

func (a activation) GetActivationToken(r *xdgactivation.ActivationV1, id uint32) {
	_, _ = xdgactivation.NewActivationTokenV1(r.Client(), r.Version(), id, &tokenRequest{server: a.server})
}

// Activate focuses the toplevel of surface when the token is valid. The
// token is used up either way.
func (a activation) Activate(_ *xdgactivation.ActivationV1, token string, surf *wayland.Surface) {
	s := a.server
	t, ok := s.tokens[token]
	delete(s.tokens, token)
	state := s.surfaceOf(surf)
	reason := ""
	switch {
	case !ok:
		reason = "unknown_token"
	case time.Since(t.at) > tokenLifetime:
		reason = "expired"
	case state == nil:
		reason = "no_surface"
	}
	var w *window
	if reason == "" {
		if w = s.windows[state.root().windowID()]; w == nil || w.toplevel == nil || !w.mapped {
			reason = "not_toplevel"
		}
	}
	if reason != "" {
		s.log.Debug().Str("reason", reason).Msg("activation refused")
		return
	}
	s.log.Info().Uint64("id", uint64(w.id)).Msg("activation")
	s.emit(ports.WindowActivate{ID: w.id})
}

// tokenRequest collects a token's serial and surface until commit.
type tokenRequest struct {
	server    *Server
	serial    uint32
	hasSerial bool
	used      bool
}

func (t *tokenRequest) SetSerial(_ *xdgactivation.ActivationTokenV1, serial uint32, _ *wayland.Seat) {
	t.serial, t.hasSerial = serial, true
}
func (*tokenRequest) SetAppId(*xdgactivation.ActivationTokenV1, string)             {}
func (*tokenRequest) SetSurface(*xdgactivation.ActivationTokenV1, *wayland.Surface) {}
func (*tokenRequest) Destroy(*xdgactivation.ActivationTokenV1)                      {}

// Commit issues the token. It is valid when the requesting client has the
// keyboard focus, or made the last press, recent and not followed by a
// focus change, that it names by serial. Invalid tokens are not kept.
func (t *tokenRequest) Commit(r *xdgactivation.ActivationTokenV1) {
	if t.used {
		r.PostError(uint32(xdgactivation.ActivationTokenV1ErrorAlreadyUsed), "token already committed")
		return
	}
	t.used = true
	s := t.server
	c := r.Client()
	fresh := time.Since(s.pressAt) <= tokenLifetime && !s.pressAt.Before(s.focusAt)
	valid := s.focusClient() == c || (t.hasSerial && t.serial == s.press && s.pressClient == c && fresh)
	token := rand.Text()
	if valid {
		s.pruneTokens()
		s.tokens[token] = activationToken{client: c, at: time.Now()}
	}
	r.SendDone(token)
}

// focusClient is the client with the keyboard focus.
func (s *Server) focusClient() server.Client {
	if w := s.windows[s.focused]; w != nil && w.xdg.resource.Resource.Alive() {
		return w.xdg.resource.Client()
	}
	if l := s.layers[s.focused]; l != nil && l.resource.Resource.Alive() {
		return l.resource.Client()
	}
	return server.Client{}
}

// pruneTokens forgets expired tokens and, past maxTokens, the oldest.
func (s *Server) pruneTokens() {
	oldest := ""
	for k, t := range s.tokens {
		if time.Since(t.at) > tokenLifetime {
			delete(s.tokens, k)
		} else if oldest == "" || t.at.Before(s.tokens[oldest].at) {
			oldest = k
		}
	}
	if len(s.tokens) >= maxTokens {
		delete(s.tokens, oldest)
	}
}

// dropTokens forgets the tokens of a client that lost the focus.
func (s *Server) dropTokens(c server.Client) {
	for k, t := range s.tokens {
		if t.client == c {
			delete(s.tokens, k)
		}
	}
}
