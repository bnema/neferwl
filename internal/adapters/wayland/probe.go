package wayland

// Ping returns once the display loop ran an empty task, or once the display
// stopped. The watchdog uses it to detect a stuck display loop.
func (s *Server) Ping() { s.display.Do(func() {}) }
