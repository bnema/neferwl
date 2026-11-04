package wayland

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// mainContent selects one buffered source, independently of commit ordering.
// Full-size subsurfaces win ties with the root (Wine keeps a static root).
// Among children, structural order breaks ties. Smaller decorations never win.
func (s *surface) mainContent() *surface {
	best := s
	area := func(s *surface) int64 {
		if !s.has || s.destroyed {
			return -1
		}
		return int64(s.content.LogicalW) * int64(s.content.LogicalH)
	}
	for _, child := range s.sub.children {
		candidate := child.mainContent()
		if area(candidate) > area(best) || best == s && area(candidate) == area(best) && area(candidate) >= 0 {
			best = candidate
		}
	}
	return best
}

// refreshWindowFacts runs after committed content changes, including detaches.
// Only the selected source contributes timings and buffer facts. Switching
// sources clears timings so intervals never straddle two independent streams.
func (s *surface) refreshWindowFacts(fresh bool) {
	r := s.root()
	id := r.windowID()
	if id == 0 {
		return
	}
	source := r.mainContent()
	managed := !r.outputColor().PlainSDR() || source.has && !source.outputColor().PlainSDR()
	if managed != r.windowColorManaged {
		r.windowColorManaged = managed
		s.server.emit(ports.WindowColorDescription{ID: id, PlainSDR: !managed})
	}
	stats := &s.server.stats
	if stats.watched != id {
		return
	}
	if stats.source != source {
		stats.source = source
		stats.client = frameRing{}
		stats.lastCommit = time.Time{}
	}
	stats.bufferFacts(source.content, source.hold.acquire != nil, source.async)
	if fresh && s == source {
		stats.commit(s.server.clock.Now(), source.content, source.hold.acquire != nil, source.async)
	}
}
