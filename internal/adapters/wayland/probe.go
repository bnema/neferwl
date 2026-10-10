package wayland

import "context"

// Ping returns once the display loop ran an empty task, the display
// stopped or ctx ended. The watchdog uses it to detect a stuck display
// loop; a stuck loop leaves only the inner Do goroutine behind.
func (s *Server) Ping(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.display.Do(func() {}); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
