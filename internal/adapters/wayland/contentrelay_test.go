package wayland

import (
	"sync"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

// relayContents attaches an output to s before Run and returns the contents it
// receives, one by one. It consumes batches like an output does: apply, then
// Return. The server must not be running yet.
func relayContents(t *testing.T, s *Server) <-chan domain.SurfaceContent {
	t.Helper()
	// Fixture wiring: the output lists the window IDs the tests create (core
	// decides this in production), with generation 0 so no gate batch is sent.
	ids := make([]domain.WindowID, 512)
	for i := range ids {
		ids[i] = domain.WindowID(i + 1)
	}
	s.router.setRoutes([]domain.ContentRoute{{Output: "HEADLESS-1", Windows: ids}})
	in, ret := s.attachOutput("HEADLESS-1")
	out := make(chan domain.SurfaceContent, 64)
	stop := make(chan struct{})
	flush := make(chan chan struct{})
	relayFlush.Store((<-chan domain.SurfaceContent)(out), flush)
	t.Cleanup(func() { close(stop); relayFlush.Delete((<-chan domain.SurfaceContent)(out)) })
	forward := func(b *domain.ContentBatch) bool {
		for _, c := range b.Items {
			select {
			case out <- c:
			case <-stop:
				return false
			}
		}
		b.Reset()
		ret.Return(b)
		return true
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			case b := <-in:
				if !forward(b) {
					return
				}
			case done := <-flush:
				// Batches the display already sent wait in `in`; forward them
				// before reporting, so the caller sees every earlier content.
				for {
					select {
					case b := <-in:
						if !forward(b) {
							return
						}
						continue
					default:
					}
					break
				}
				close(done)
			}
		}
	}()
	return out
}

// relayFlush maps a relayContents channel to its flush request channel.
var relayFlush sync.Map

// flushRelay returns once the relay forwarded every batch the display sent
// before the call (the caller already crossed a display.Do barrier).
func flushRelay(t *testing.T, contents <-chan domain.SurfaceContent) {
	t.Helper()
	f, ok := relayFlush.Load(contents)
	if !ok {
		t.Fatal("contents channel has no relay")
	}
	done := make(chan struct{})
	f.(chan chan struct{}) <- done
	<-done
}
