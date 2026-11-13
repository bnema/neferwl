package wayland

import (
	"time"

	"github.com/rs/zerolog"
)

// headBlock is recorded with the cached readiness result, without formatting
// strings or allocating on the commit path.
type headBlock uint8

const (
	blockNone headBlock = iota
	blockBarrier
	blockTimestamp
	blockAcquire
	blockImplicit
	blockSynced
	blockPrev
	blockDeps
)

func (b headBlock) String() string {
	switch b {
	case blockBarrier:
		return "barrier"
	case blockTimestamp:
		return "timestamp"
	case blockAcquire:
		return "acquire"
	case blockImplicit:
		return "implicit"
	case blockSynced:
		return "synced"
	case blockPrev:
		return "prev"
	case blockDeps:
		return "deps"
	default:
		return "none"
	}
}

func (s *surface) headState(now time.Time) (time.Duration, headBlock) {
	if len(s.queue) == 0 {
		return 0, blockNone
	}
	u := s.queue[0]
	if u.synced {
		return max(now.Sub(u.enqueued), 0), blockSynced
	}
	return max(now.Sub(u.enqueued), 0), u.blocked
}

// logSurfaceState runs only on the display goroutine. Stall messages leave the
// interval baselines untouched so the next stats line still covers all activity.
func (s *surface) logSurfaceState(now time.Time, stalled bool) {
	age, block := s.headState(now)
	level := zerolog.InfoLevel
	if stalled {
		level = zerolog.WarnLevel
	}
	e := s.server.log.WithLevel(level)
	e = e.Uint64("window", uint64(s.windowID())).
		Uint64("commits_received", s.commitsReceived-s.statsReceived).
		Uint64("commits_applied", s.commitsApplied-s.statsApplied).
		Int("queue_len", len(s.queue)).
		Int64("head_age_ms", age.Milliseconds()).
		Str("head_block", block.String()).
		Uint64("release_points_signalled", s.releasePointsSignalled-s.statsSignalled).
		Uint64("release_points_dropped", s.releasePointsDropped-s.statsDropped)
	if stalled {
		if block == blockAcquire {
			cs := s.queue[0].sync
			if cs != nil && cs.acquire.set() {
				e = e.Uint64("acquire_point", cs.acquire.point).Uint32("acquire_timeline", cs.acquire.tl.handle)
			}
		}
		e.Msg("surface commit stalled")
		return
	}
	e.Msg("surface stats")
}

func (s *surface) reportStall(now time.Time) {
	if s.windowID() == 0 || len(s.queue) == 0 {
		return
	}
	u := s.queue[0]
	age, block := s.headState(now)
	// FIFO barriers intentionally wait for refresh and are not evidence of a
	// wedged acquire. A later non-barrier block can still warn for this head.
	if u.stalled || age <= 500*time.Millisecond || block == blockBarrier || block == blockNone {
		return
	}
	u.stalled = true
	s.logSurfaceState(now, true)
}

// reportSurfaceStats is posted by the existing ten-second command stats timer.
// It does not evaluate readiness (which can cancel waits), apply, or wake work.
func (s *Server) reportSurfaceStats(now time.Time) {
	for _, surf := range s.surfaces {
		if surf.windowID() != 0 && (surf.commitsReceived != surf.statsReceived || len(surf.queue) != 0) {
			surf.reportStall(now)
			surf.logSurfaceState(now, false)
		}
		surf.statsReceived, surf.statsApplied = surf.commitsReceived, surf.commitsApplied
		surf.statsSignalled, surf.statsDropped = surf.releasePointsSignalled, surf.releasePointsDropped
	}
}
