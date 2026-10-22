package wayland

import (
	"context"

	"bnema.dev/purego-libwayland/server"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/wayland/xwm"
	"golang.org/x/sys/unix"
)

// xwaylandClient belongs to the display goroutine.
type xwaylandClient struct {
	client server.Client
}

// AttachXwayland transfers ownership of the Wayland descriptor to the display.
// It may be called from any goroutine.
func (s *Server) AttachXwayland(wlFD int) {
	ran := s.display.Do(func() {
		c, err := s.display.CreateClient(wlFD)
		if err != nil {
			s.log.Warn().Err(err).Msg("Xwayland connection not adopted")
			_ = unix.Close(wlFD) // CreateClient only adopts on success.
			return
		}
		s.forgetXwayland()
		s.xwayland = xwaylandClient{client: c}
		c.OnDestroy(func() {
			if s.xwayland.client == c {
				s.teardownXWM()
				s.forgetXwayland()
			}
		})
		s.log.Info().Msg("Xwayland attached")
	})
	if !ran {
		_ = unix.Close(wlFD)
	}
}

// forgetXwayland runs on the display owner, or after Run has joined it.
func (s *Server) forgetXwayland() {
	if s.xwayland.client != (server.Client{}) {
		s.xwayland = xwaylandClient{}
	}
}

// AttachXWM bootstraps the X11 window manager on the window manager
// descriptor, which it owns. It runs on the starter goroutine once Xwayland is
// ready and blocks until the XWM is installed, ctx ends or Boot fails. stop
// closes the XWM on the display goroutine and waits for it.
func (s *Server) AttachXWM(ctx context.Context, wmFD int) (stop func()) {
	log := logging.For(ctx, "xwm")
	x, err := xwm.Boot(ctx, wmFD, log)
	if err != nil {
		if ctx.Err() != nil {
			log.Info().Err(err).Msg("X11 window management not started: Xwayland is going away")
		} else {
			log.Warn().Err(err).Msg("X11 window management disabled")
		}
		return func() {}
	}
	if !s.display.Do(func() { s.installXWM(x) }) {
		x.CloseConn()
		return func() {}
	}
	return func() {
		s.display.Do(func() {
			if s.xwm == x {
				s.teardownXWM()
			}
		})
	}
}
