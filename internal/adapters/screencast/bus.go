package screencast

import "github.com/bnema/zerobus"

// request is a decoded method call: every string is copied and capped, so it
// outlives the receive buffer while it waits for the caller check.
type request struct {
	sender string
	serial uint32
	noWait bool // the caller expects no reply
}

// session is one org.gnome.Mutter.ScreenCast.Session.
type session struct {
	used  bool
	owner string
}

func (s *Service) onMessage(m *zerobus.Message) { _ = m }

func (s *Service) forEachSession(f func(*session)) {
	for i := range s.sessions {
		if s.sessions[i].used {
			f(&s.sessions[i])
		}
	}
}

func (s *Service) closeSession(sess *session, signal bool) { _, _ = sess, signal }
