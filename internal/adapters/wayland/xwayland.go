package wayland

import (
	"bnema.dev/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// xwaylandClient belongs to the display goroutine. Phase 6 takes wmFD here
// to run the XWM; until then it is held open without dispatching X11 traffic.
type xwaylandClient struct {
	client server.Client
	wmFD   int
}

// AttachXwayland transfers ownership of both descriptors to the display.
// It may be called from any goroutine; Phase 6 takes the XWM fd from here.
func (s *Server) AttachXwayland(wlFD, wmFD int) {
	ran := s.display.Do(func() {
		c, err := s.display.CreateClient(wlFD)
		if err != nil {
			s.log.Warn().Err(err).Msg("Xwayland connection not adopted")
			_ = unix.Close(wlFD) // CreateClient only adopts on success.
			_ = unix.Close(wmFD)
			return
		}
		s.forgetXwayland()
		s.xwayland = xwaylandClient{client: c, wmFD: wmFD}
		c.OnDestroy(func() {
			if s.xwayland.client == c {
				s.forgetXwayland()
			}
		})
		s.log.Info().Msg("Xwayland attached")
	})
	if !ran {
		_ = unix.Close(wlFD)
		_ = unix.Close(wmFD)
	}
}

// forgetXwayland runs on the display owner, or after Run has joined it.
func (s *Server) forgetXwayland() {
	if s.xwayland.client != (server.Client{}) {
		_ = unix.Close(s.xwayland.wmFD)
		s.xwayland = xwaylandClient{}
	}
}
