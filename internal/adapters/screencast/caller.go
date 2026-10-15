package screencast

import (
	"os"

	"github.com/bnema/zerobus"
)

// The caller check proves which executable a D-Bus peer runs, with the pidfd
// proof the wayland adapter uses for its clients (package peerexe):
//
//  1. GetConnectionUnixProcessID(name) names a pid;
//  2. pidfd_open(pid) pins that process;
//  3. a second GetConnectionUnixProcessID must name the same pid: the name
//     was connected throughout, so the pidfd names the peer and not a process
//     that recycled the pid;
//  4. the exe is read through the pidfd.
//
// The bus calls are sent with Send and their replies come back through
// ReadMessage, matched by serial: nothing here waits. A request that arrives
// while its sender is being checked waits in the pending table.

// callerState is how far the proof of a D-Bus peer's executable got.
type callerState uint8

const (
	callerFree callerState = iota
	// callerPid: waiting for the bus to name the peer's pid.
	callerPid
	// callerConfirm: a pidfd was opened; waiting for the bus to name the same
	// pid again.
	callerConfirm
	// callerKnown: exe is the executable of the peer, and the allowlist
	// admitted it when it was proven. Only such peers are cached.
	callerKnown
)

// caller is one checked unique bus name. The pidfd stays open for as long
// as the name is cached, so the executable can be read again at the
// privileged entry points.
type caller struct {
	state  callerState
	name   string // unique name, ":1.42"
	serial uint32 // the outstanding GetConnectionUnixProcessID request
	pid    int
	pidfd  *os.File
	exe    string
}

func (c *caller) release() {
	if c.pidfd != nil {
		_ = c.pidfd.Close()
	}
	*c = caller{}
}

// pending is a request parked until its sender's check ends.
type pending struct {
	used bool
	req  request
}

// callerOf returns the cache entry of a unique name.
func (s *Service) callerOf(name string) *caller {
	for i := range s.callers {
		if s.callers[i].state != callerFree && s.callers[i].name == name {
			return &s.callers[i]
		}
	}
	return nil
}

// admit takes a decoded call: a checked sender is served at once, any other
// waits for its check.
func (s *Service) admit(req *request) {
	c := s.callerOf(req.sender)
	if c != nil && c.state == callerKnown {
		if !s.allowed(c.exe) {
			// The policy closed on a cached peer: refuse and forget it.
			s.deny(req)
			c.release()
			return
		}
		s.dispatch(c, req)
		return
	}
	slot := -1
	for i := range s.pending {
		if !s.pending[i].used {
			slot = i
			break
		}
	}
	if slot < 0 {
		s.replyErr(req, errLimits, "too many requests wait for a caller check")
		return
	}
	if c == nil {
		for i := range s.callers {
			if s.callers[i].state == callerFree {
				c = &s.callers[i]
				break
			}
		}
		if c == nil {
			s.replyErr(req, errLimits, "too many callers")
			return
		}
		*c = caller{state: callerPid, name: req.sender}
		if !s.askPid(c) {
			c.release()
			s.deny(req)
			return
		}
	}
	s.pending[slot] = pending{used: true, req: *req}
}

// askPid sends GetConnectionUnixProcessID for c and keeps its serial.
func (s *Service) askPid(c *caller) bool {
	e, serial := s.conn.NewMethodCall(busDaemon, busDaemonPath, busDaemon, "GetConnectionUnixProcessID", "s")
	e.Str(c.name)
	if _, err := s.conn.Send(); err != nil {
		s.fatal = err
		return false
	}
	c.serial = serial
	return true
}

// onBusReply advances the check of the caller the reply answers; any other
// reply of the bus is not ours.
func (s *Service) onBusReply(m *zerobus.Message) {
	var c *caller
	for i := range s.callers {
		cc := &s.callers[i]
		if (cc.state == callerPid || cc.state == callerConfirm) && cc.serial == m.ReplySerial {
			c = cc
			break
		}
	}
	if c == nil {
		return
	}
	pid := -1
	if m.Type == zerobus.TypeMethodReturn && m.Signature == "u" {
		r := m.Body()
		v := r.Uint32()
		if r.Err() == nil && v > 0 && v < 1<<31 {
			pid = int(v)
		}
	}
	switch {
	case pid < 0:
		s.failCaller(c, "pid lookup failed")
	case c.state == callerPid:
		pidfd, err := s.peers.Open(pid)
		if err != nil {
			s.log.Info().Err(err).Str("sender", c.name).Msg("caller refused: no pidfd")
			s.failCaller(c, "")
			return
		}
		c.pid, c.pidfd, c.state = pid, pidfd, callerConfirm
		if !s.askPid(c) {
			s.failCaller(c, "")
		}
	default:
		if pid != c.pid {
			s.log.Info().Str("sender", c.name).Msg("caller refused: pid changed during the check")
			s.failCaller(c, "")
			return
		}
		exe, got, err := s.peers.Exe(c.pidfd)
		if err != nil || got != c.pid {
			s.log.Info().Err(err).Str("sender", c.name).Msg("caller refused: executable not proven")
			s.failCaller(c, "")
			return
		}
		if !s.allowed(exe) {
			// A refused peer is not cached: it would hold one of the
			// maxCallers entries until it leaves the bus, and enough of
			// them would lock the real portal out. It is checked again,
			// from a free entry, if it calls again.
			s.log.Info().Str("sender", c.name).Str("exe", exe).Msg("caller refused: executable not allowed")
			s.failCaller(c, "")
			return
		}
		c.exe, c.state = exe, callerKnown
		s.flush(c)
	}
}

// failCaller refuses every request that waits for c and forgets it.
func (s *Service) failCaller(c *caller, why string) {
	if why != "" {
		s.log.Info().Str("sender", c.name).Msg("caller refused: " + why)
	}
	name := c.name
	c.release()
	for i := range s.pending {
		if p := &s.pending[i]; p.used && p.req.sender == name {
			req := p.req
			*p = pending{}
			s.deny(&req)
		}
	}
}

// flush serves the requests that waited for c, in arrival order.
func (s *Service) flush(c *caller) {
	for i := range s.pending {
		p := &s.pending[i]
		if !p.used || p.req.sender != c.name {
			continue
		}
		req := p.req
		*p = pending{}
		s.dispatch(c, &req)
	}
}

// onGone forgets a peer that left the bus: its check, its parked requests
// and its sessions.
func (s *Service) onGone(name string) {
	if c := s.callerOf(name); c != nil {
		c.release()
	}
	for i := range s.pending {
		if s.pending[i].used && s.pending[i].req.sender == name {
			s.pending[i] = pending{}
		}
	}
	for i := range s.sessions {
		if sess := &s.sessions[i]; sess.used && sess.owner == name {
			s.closeSession(sess, false)
		}
	}
}

// allowed reports whether the allowlist in force admits exe.
func (s *Service) allowed(exe string) bool {
	p := s.policy()
	return p != nil && p.Allows(exe)
}

// recheck reads the executable behind the pidfd again, so an exec after the
// first check is caught, and applies the allowlist to it.
func (s *Service) recheck(c *caller) bool {
	exe, pid, err := s.peers.Exe(c.pidfd)
	if err != nil || pid != c.pid {
		s.log.Info().Err(err).Str("sender", c.name).Msg("caller refused: executable not proven")
		return false
	}
	if exe != c.exe {
		s.log.Info().Str("sender", c.name).Str("was", c.exe).Str("now", exe).Msg("caller executable changed")
		c.exe = exe
	}
	return s.allowed(exe)
}
