package wayland

import (
	"errors"
	"strings"
	"time"

	"github.com/bnema/purego-libwayland/protocol/securitycontext"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// wp_security_context_v1: a sandbox engine (Flatpak) asks the compositor to
// serve a listening socket it made, and everything that connects to it is a
// sandboxed client. Such a client is tagged when it is adopted and cannot
// capture the screen (mayCapture): it asks the desktop portal, whose own
// connection is not sandboxed. Sandboxed clients cannot nest a context.
//
// The display has no per-client global filter, so the capture globals stay
// advertised to sandboxed clients and their requests are refused; so does
// the manager global, which a sandboxed client may bind (binding every global
// is common) but not use: create_listener is the nested protocol error. Only capture is
// restricted for sandboxed clients so far: other privileged protocols
// (virtual keyboard, output management, data control, foreign toplevels...)
// are not.
//
// Each committed context runs one goroutine that polls its listening socket
// (the event loop of the display is not reachable from Go), accepts, and hands
// each connection to the display goroutine, which adopts it. The goroutine
// stops when close_fd hangs up or when the compositor stops, and then closes
// both descriptors. It keeps serving when the client that committed the
// context goes away (the protocol says so).

// sandbox is what a sandboxed client was committed with. Engine, app and
// instance ids are the sandbox's own claims; they label logs and nothing else.
type sandbox struct {
	engine, appID, instanceID string
}

const (
	maxSecurityMetadata = 1024 // bytes per metadata string
	acceptRetry         = 50 * time.Millisecond
)

func registerSecurityContext(d *server.Display, s *Server) error {
	return securitycontext.NewWpSecurityContextManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = securitycontext.NewWpSecurityContextManagerV1(c, int32(v), id, &securityManager{s: s})
	})
}

type securityManager struct{ s *Server }

func (*securityManager) Destroy(*securitycontext.WpSecurityContextManagerV1) {}

func (m *securityManager) CreateListener(r *securitycontext.WpSecurityContextManagerV1, id uint32, listenFD, closeFD int) {
	s := m.s
	switch {
	case s.sandboxed[r.Client()] != nil:
		r.PostError(uint32(securitycontext.WpSecurityContextManagerV1ErrorNested), "nested security contexts are forbidden")
	case !listeningUnixSocket(listenFD):
		r.PostError(uint32(securitycontext.WpSecurityContextManagerV1ErrorInvalidListenFd), "listen_fd is not a listening unix stream socket")
	default:
		ctx := &securityContext{s: s, listenFD: listenFD, closeFD: closeFD}
		res, err := securitycontext.NewWpSecurityContextV1(r.Client(), r.Version(), id, ctx)
		if err == nil {
			// The client went away, or destroyed the object, before commit.
			res.OnDestroy = ctx.release
			return
		}
	}
	_ = unix.Close(listenFD)
	_ = unix.Close(closeFD)
}

// listeningUnixSocket reports whether fd is a unix stream socket that listens.
func listeningUnixSocket(fd int) bool {
	if fd < 0 {
		return false
	}
	if v, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN); err != nil || v == 0 {
		return false
	}
	if v, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN); err != nil || v != unix.AF_UNIX {
		return false
	}
	v, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	return err == nil && v == unix.SOCK_STREAM
}

// securityContext is one wp_security_context_v1. It owns the descriptors
// until commit hands them to the listener.
type securityContext struct {
	s                 *Server
	listenFD, closeFD int
	info              sandbox
	engine, app, inst bool // set once
	committed         bool
}

// release closes the descriptors of a context that was never committed.
func (c *securityContext) release() {
	if c.committed {
		return
	}
	c.committed = true
	_ = unix.Close(c.listenFD)
	_ = unix.Close(c.closeFD)
}

func (c *securityContext) Destroy(*securitycontext.WpSecurityContextV1) {}

// usable rejects any request after commit.
func (c *securityContext) usable(r *securitycontext.WpSecurityContextV1) bool {
	if c.committed {
		r.PostError(uint32(securitycontext.WpSecurityContextV1ErrorAlreadyUsed), "security context already committed")
		return false
	}
	return true
}

// validMetadata: non-empty, bounded, printable-enough (no NUL or control).
func validMetadata(v string) bool {
	return v != "" && len(v) <= maxSecurityMetadata && !strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func (c *securityContext) set(r *securitycontext.WpSecurityContextV1, done *bool, dst *string, v string) {
	switch {
	case !c.usable(r):
	case *done:
		r.PostError(uint32(securitycontext.WpSecurityContextV1ErrorAlreadySet), "metadata already set")
	case !validMetadata(v):
		r.PostError(uint32(securitycontext.WpSecurityContextV1ErrorInvalidMetadata), "metadata must be a non-empty printable string of at most 1024 bytes")
	default:
		*done, *dst = true, v
	}
}

func (c *securityContext) SetSandboxEngine(r *securitycontext.WpSecurityContextV1, name string) {
	c.set(r, &c.engine, &c.info.engine, name)
}
func (c *securityContext) SetAppId(r *securitycontext.WpSecurityContextV1, id string) {
	c.set(r, &c.app, &c.info.appID, id)
}
func (c *securityContext) SetInstanceId(r *securitycontext.WpSecurityContextV1, id string) {
	c.set(r, &c.inst, &c.info.instanceID, id)
}

// Commit starts serving the listening socket.
func (c *securityContext) Commit(r *securitycontext.WpSecurityContextV1) {
	if !c.usable(r) {
		return
	}
	// The engine name is the one thing every sandbox has; without it a
	// context says nothing about who is behind it.
	if !c.engine {
		r.PostError(uint32(securitycontext.WpSecurityContextV1ErrorInvalidMetadata), "sandbox engine not set")
		return
	}
	c.committed = true
	l := &securityListener{s: c.s, listenFD: c.listenFD, closeFD: c.closeFD, info: c.info}
	c.s.securityLive.Add(1)
	c.s.securityWG.Add(1)
	go l.run()
	c.s.log.Info().Str("engine", c.info.engine).Str("app_id", c.info.appID).Msg("security context listener started")
}

// securityListener accepts the connections of one committed context.
type securityListener struct {
	s                 *Server
	listenFD, closeFD int
	info              sandbox
}

func (l *securityListener) run() {
	s := l.s
	defer s.securityWG.Done()
	defer s.securityLive.Add(-1)
	defer unix.Close(l.closeFD)
	defer unix.Close(l.listenFD)
	// Only this listener accepts on the socket: a blocked accept must not
	// outlast a stop request.
	if err := unix.SetNonblock(l.listenFD, true); err != nil {
		s.log.Warn().Err(err).Str("engine", l.info.engine).Msg("security context listener: socket not usable")
		return
	}
	for {
		fds := []unix.PollFd{{Fd: int32(l.listenFD), Events: unix.POLLIN}, {Fd: int32(l.closeFD), Events: unix.POLLIN}, {Fd: int32(s.securityStop), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			s.log.Warn().Err(err).Str("engine", l.info.engine).Msg("security context listener: poll")
			return
		}
		switch {
		case fds[2].Revents != 0:
			return // the compositor stops
		case fds[1].Revents != 0: // readable or hung up
			s.log.Info().Str("engine", l.info.engine).Str("app_id", l.info.appID).Msg("security context listener closed")
			return
		case fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0:
			s.log.Warn().Str("engine", l.info.engine).Msg("security context listener: socket failed")
			return
		}
		if fds[0].Revents&unix.POLLIN != 0 && !l.acceptAll() {
			return
		}
	}
}

// acceptAll adopts every pending connection; it returns false to stop.
func (l *securityListener) acceptAll() bool {
	s := l.s
	for {
		fd, _, err := unix.Accept4(l.listenFD, unix.SOCK_CLOEXEC)
		switch {
		case err == nil:
			l.adopt(fd)
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.ECONNABORTED):
			return true
		case errors.Is(err, unix.EINTR):
		case errors.Is(err, unix.EMFILE), errors.Is(err, unix.ENFILE), errors.Is(err, unix.ENOMEM), errors.Is(err, unix.ENOBUFS):
			// Out of descriptors: wait for some, unless told to stop.
			s.log.Warn().Err(err).Msg("security context listener: accept")
			fds := []unix.PollFd{{Fd: int32(s.securityStop), Events: unix.POLLIN}}
			if _, perr := unix.Poll(fds, int(acceptRetry.Milliseconds())); perr == nil && fds[0].Revents != 0 {
				return false
			}
			return true
		default:
			s.log.Warn().Err(err).Msg("security context listener: accept")
			return false
		}
	}
}

// adopt hands a connection to the display goroutine as a sandboxed client.
func (l *securityListener) adopt(fd int) {
	s := l.s
	ran := s.display.Do(func() {
		c, err := s.display.CreateClient(fd)
		if err != nil {
			s.log.Warn().Err(err).Msg("security context: connection not adopted")
			_ = unix.Close(fd)
			return
		}
		info := l.info
		s.sandboxed[c] = &info
		c.OnDestroy(func() { delete(s.sandboxed, c) })
		s.log.Debug().Str("engine", info.engine).Str("app_id", info.appID).Msg("sandboxed client connected")
	})
	if !ran {
		_ = unix.Close(fd) // the display stopped first
	}
}

// stopSecurityContexts ends every listener and waits for them. Called once
// the display has stopped.
func (s *Server) stopSecurityContexts() {
	var one = [8]byte{1}
	_, _ = unix.Write(s.securityStop, one[:])
	s.securityWG.Wait()
}
