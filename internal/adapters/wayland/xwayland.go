package wayland

import (
	"bnema.dev/purego-libwayland/server"
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

// AttachXWM takes ownership of the window manager descriptor. It runs on the
// starter goroutine once Xwayland is ready; the XWM is not implemented yet,
// so the descriptor is closed and stop does nothing.
func (s *Server) AttachXWM(wmFD int) (stop func()) {
	s.log.Info().Msg("X11 window management not implemented: closing XWM descriptor")
	_ = unix.Close(wmFD)
	return func() {}
}
