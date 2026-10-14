package screencast

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// stream is one cast of a session.
type stream struct {
	used  bool
	dirty bool
}

func (s *Service) onCastEvent(ev *ports.CastEvent) { _ = ev }

func (s *Service) forEachStream(f func(*stream)) {
	for i := range s.sessions {
		_ = f
		_ = i
	}
}

func (s *Service) pumpAll(now time.Time) {}
func (s *Service) reap()                 {}
func (s *Service) streamsLive() bool     { return false }
