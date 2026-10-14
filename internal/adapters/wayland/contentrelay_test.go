package wayland

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// relayContents attaches an output to s before Run and returns the contents it
// receives, one by one. It consumes batches like an output does: apply, then
// Return. The server must not be running yet.
func relayContents(t *testing.T, s *Server) <-chan ports.SurfaceContent {
	t.Helper()
	in, ret := s.attachOutput("TEST-1")
	out := make(chan ports.SurfaceContent, 64)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case b := <-in:
				for _, c := range b.Items {
					select {
					case out <- c:
					case <-stop:
						return
					}
				}
				b.Reset()
				ret.Return(b)
			}
		}
	}()
	return out
}
