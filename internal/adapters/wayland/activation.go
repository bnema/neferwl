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

// tokenLifetime is how long a token can activate a window.
const tokenLifetime = 10 * time.Second

// activationToken is an issued token; valid ones may activate a window.
type activationToken struct {
	valid bool
	at    time.Time
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
	if !ok || !t.valid || time.Since(t.at) > tokenLifetime || state == nil {
		s.log.Debug().Bool("known", ok).Msg("activation refused")
		return
	}
	w := s.windows[state.root().windowID()]
	if w == nil || w.toplevel == nil || !w.mapped {
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
// keyboard focus or made the last press it names by serial.
func (t *tokenRequest) Commit(r *xdgactivation.ActivationTokenV1) {
	if t.used {
		r.PostError(uint32(xdgactivation.ActivationTokenV1ErrorAlreadyUsed), "token already committed")
		return
	}
	t.used = true
	s := t.server
	c := r.Client()
	valid := s.focusClient() == c || (t.hasSerial && t.serial == s.press && s.pressClient == c)
	s.pruneTokens()
	token := rand.Text()
	s.tokens[token] = activationToken{valid: valid, at: time.Now()}
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

// pruneTokens forgets expired tokens.
func (s *Server) pruneTokens() {
	for k, t := range s.tokens {
		if time.Since(t.at) > tokenLifetime {
			delete(s.tokens, k)
		}
	}
}
