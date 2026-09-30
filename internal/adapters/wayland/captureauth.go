package wayland

import (
	"errors"
	"os"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/purego-libwayland/server"
)

// mayCapture is the one place that decides whether a client may capture the
// screen. Every capture path asks it: wlr-screencopy frames, ext-image-copy-
// capture sessions and frames, whatever their source (output, output region
// or workspace), and the attach of a layer surface to a capture exclusion. A
// refused client gets the failure its protocol defines (failed, or stopped
// for a session) and nothing is captured or flashed. In order:
//
//  1. While the session is protected (ext-session-lock) no client may
//     capture, so nothing of the locked session reaches a buffer.
//  2. A sandboxed client (one that connected through a
//     wp_security_context_v1 listener, securitycontext.go) never captures,
//     whatever the allowlist says: it goes through the desktop portal. The
//     display cannot hide globals per client, so the capture globals stay
//     visible to it and its requests are refused here.
//  3. The executable allowlist (captureallow; /etc/neferwl/capture-allow): no
//     `*` line lets every client through, an invalid file lets none,
//     otherwise the executable actually running behind the client's
//     connection must be listed (in the file, or in the built-in list while
//     the file is missing). captureexe.go resolves it afresh at every
//     wlr-screencopy frame request and ext session creation, from a pidfd kept
//     per client; the frames of an ext session use mayCaptureFrame. Without
//     SO_PEERPIDFD nobody is identified, so nobody captures.
//
// Display goroutine only.
func (s *Server) mayCapture(c server.Client) bool {
	return s.mayCaptureWith(c, s.peerIdentity)
}

// mayCaptureFrame is mayCapture for the frames of an ext-image-copy-capture
// session: a frame does not read /proc again but uses the executable resolved
// at the client's latest check (the session's creation, or a later wlr request
// or session on the same connection). The current policy and the lock are
// still checked each time. Display goroutine only.
func (s *Server) mayCaptureFrame(c server.Client) bool {
	return s.mayCaptureWith(c, func(c server.Client) *peerID {
		id := s.peerEntry(c)
		if id.exe == "" && id.err == nil {
			// Never resolved: the policy was off when the session began.
			id = s.peerIdentity(c)
		}
		return id
	})
}

// mayCaptureWith is the rule chain of mayCapture; resolve names the client's
// executable once the policy needs it.
func (s *Server) mayCaptureWith(c server.Client, resolve func(server.Client) *peerID) bool {
	if s.protected() {
		return false
	}
	if s.sandboxed[c] != nil {
		s.refuseOnce(c, "sandboxed client", "", 0)
		return false
	}
	if s.captureAllow == nil {
		// No allowlist store: fail closed, never allow everyone.
		s.refuseOnce(c, "allowlist invalid", "", 0)
		return false
	}
	pol := s.captureAllow.Policy()
	switch pol.Mode() {
	case captureallow.ModeDisabled:
		return true
	case captureallow.ModeClosed:
		s.refuseOnce(c, "allowlist invalid", "", 0)
		return false
	}
	id := resolve(c)
	if id.err != nil {
		s.refuseOnce(c, "executable unknown: "+id.err.Error(), "", id.pid)
		return false
	}
	if !pol.Allows(id.exe) {
		s.refuseOnce(c, "executable not in allowlist", id.exe, id.pid)
		return false
	}
	return true
}

// peerID is what is known of a client, dropped when it is destroyed. Only the
// pidfd is kept across decisions: the executable is read again at each one,
// so an exec after connecting is seen.
type peerID struct {
	pidfd *os.File // pins the peer process; closed with the client
	exe   string   // the last resolution, for mayCaptureFrame
	pid   int
	err   error
	// refused holds the refusals already logged (reason and executable), so
	// a client is logged once per distinct executable; bounded.
	refused map[string]struct{}
}

// maxRefusalsLogged bounds peerID.refused.
const maxRefusalsLogged = 8

// peerEntry is the entry of c, dropped when the client is destroyed.
func (s *Server) peerEntry(c server.Client) *peerID {
	if id := s.peers[c]; id != nil {
		return id
	}
	id := &peerID{}
	s.peers[c] = id
	c.OnDestroy(func() {
		if id.pidfd != nil {
			_ = id.pidfd.Close()
		}
		delete(s.peers, c)
	})
	return id
}

// peerIdentity resolves the executable behind c now: the pidfd is opened on
// the first call and kept, /proc/<pid>/exe and the pidfd's liveness are read
// on every call. Failures are not cached.
func (s *Server) peerIdentity(c server.Client) *peerID {
	id := s.peerEntry(c)
	id.exe, id.err = "", nil
	cred, _ := c.Credentials()
	id.pid = cred.PID
	if id.pidfd == nil {
		fd, err := c.FD()
		if err != nil {
			id.err = err
			return id
		}
		id.pidfd, err = s.peer.Pidfd(fd)
		if err != nil {
			id.pidfd, id.err = nil, err
			if errors.Is(err, errNoPidfd) && !s.noPidfdWarned {
				s.noPidfdWarned = true
				s.log.Warn().Msg("kernel has no SO_PEERPIDFD (Linux 6.5+): the capture allowlist cannot identify clients, so none may capture; list `*` in the allowlist to disable the check")
			}
			return id
		}
	}
	exe, pid, err := s.peer.Exe(id.pidfd)
	id.exe, id.err = exe, err
	if pid > 0 {
		id.pid = pid
	}
	return id
}

// refuseOnce logs a refused capture, once per client and executable:
// component, pid and executable path, never a token or anything a client
// sent.
func (s *Server) refuseOnce(c server.Client, reason, exe string, pid int) {
	id := s.peerEntry(c)
	key := reason + "\x00" + exe
	if _, done := id.refused[key]; done || len(id.refused) >= maxRefusalsLogged {
		return
	}
	if id.refused == nil {
		id.refused = map[string]struct{}{}
	}
	id.refused[key] = struct{}{}
	if pid == 0 {
		if cred, err := c.Credentials(); err == nil {
			pid = cred.PID
		}
	}
	e := s.log.Info().Int("pid", pid)
	if exe != "" {
		e = e.Str("exe", exe)
	}
	e.Str("reason", reason).Msg("capture refused")
}
