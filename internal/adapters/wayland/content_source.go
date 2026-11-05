package wayland

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// mainContent caches the source on the root. Only committed render layouts
// participate; pending structural children are not visible content yet.
func (s *surface) mainContent() *surface {
	r := s.root()
	if r.contentSource == nil {
		r.contentSource = r.selectContent(r)
	}
	return r.contentSource
}

func contentArea(s *surface) int64 {
	if !s.has || s.destroyed {
		return -1
	}
	return int64(s.content.LogicalW) * int64(s.content.LogicalH)
}

// selectContent follows appendTree's paint order. First child wins equal-area
// child ties; a full-size child wins a tie with the static root.
func (s *surface) selectContent(best *surface) *surface {
	for _, item := range s.sub.layout {
		if item.below {
			best = item.child.selectContent(best)
		}
	}
	if contentArea(s) > contentArea(best) || best == s.root() && s != best && contentArea(s) == contentArea(best) && contentArea(s) >= 0 {
		best = s
	}
	for _, item := range s.sub.layout {
		if !item.below {
			best = item.child.selectContent(best)
		}
	}
	return best
}

func (s *surface) colorManaged() bool {
	source := s.mainContent()
	return !s.outputColor().PlainSDR() || source.has && !source.outputColor().PlainSDR()
}

// mappedColor synchronizes the delta cache with the initial/remap publication.
func (s *surface) mappedColor() bool {
	s.windowColorManaged = s.colorManaged()
	return s.windowColorManaged
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
	managed := r.colorManaged()
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
