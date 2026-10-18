package wayland

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// commandRingSize bounds the client commands waiting for the display
// goroutine. It absorbs a few display frames of a 1 kHz mouse and a burst of
// key/button commands while the display parses client requests; only the
// forwarder goroutine waits when it is full, so core never blocks on the
// display directly, only on the Commands channel (cap 32) behind the forwarder.
const commandRingSize = 256

// queuedCommand is a ClientCommand in the command ring. Pointer motion, bare
// or wrapped in a SecurityCommand (core wraps every one in production), keeps
// its value and envelope state inline so folding a motion into a queued one
// allocates nothing; the command is boxed again only if a merge happened, at
// apply time, once per merged run.
type queuedCommand struct {
	cmd    ports.ClientCommand
	motion ports.PointerMotionTo // valid if isMotion
	state  ports.SecurityState   // envelope state, valid if enveloped
	// isMotion: cmd is a PointerMotionTo, bare or directly inside a
	// SecurityCommand (then enveloped is set).
	isMotion, enveloped bool
	// merged: motion holds the sum of several motions and cmd is stale.
	merged bool
	// at is when the command entered the ring path. A merge keeps the oldest
	// value, so a folded motion reports the wait of the first motion in it.
	at time.Time
}

func queue(cmd ports.ClientCommand) queuedCommand {
	q := queuedCommand{cmd: cmd, at: time.Now()}
	if env, ok := cmd.(ports.SecurityCommand); ok {
		q.state, q.enveloped = env.State, true
		cmd = env.Command
	}
	q.motion, q.isMotion = cmd.(ports.PointerMotionTo)
	return q
}

func (q *queuedCommand) command() ports.ClientCommand {
	switch {
	case !q.merged:
		return q.cmd
	case q.enveloped:
		return ports.SecurityCommand{State: q.state, Command: q.motion}
	}
	return q.motion
}

// mergeMotion folds v into last when both are pointer motion for the same
// window, both bare or both enveloped with an identical security state, and
// last is the most recent queued command, so no other command is ever
// overtaken and a bare motion never takes the place of an enveloped one. The
// position is the latest; relative deltas add up so locked pointers (games)
// lose no movement. last.at stays: the merge reports its first motion's wait.
func mergeMotion(last *queuedCommand, v queuedCommand) bool {
	if !last.isMotion || !v.isMotion || last.motion.ID != v.motion.ID ||
		last.enveloped != v.enveloped || last.state != v.state {
		return false
	}
	m := v.motion
	m.DX += last.motion.DX
	m.DY += last.motion.DY
	m.UnaccelDX += last.motion.UnaccelDX
	m.UnaccelDY += last.motion.UnaccelDY
	last.motion, last.merged = m, true
	return true
}

// newCommandRing returns the ring whose commands apply runs on the display
// goroutine, in arrival order. Adjacent motions for one window are coalesced
// at push time, against the last queued command only, under the ring mutex.
// That also coalesces while the display is busy (a drain-side batch would
// only see what the channel held at that moment), and a merge needs no room.
func newCommandRing(d *server.Display, apply func(queuedCommand)) *postRing[queuedCommand] {
	return newMergingPostRing(d, commandRingSize, apply, mergeMotion)
}

// commandStats counts what the display goroutine applied. The display
// goroutine writes and the stats logger takes, so the fields are atomic.
type commandStats struct {
	applied    atomic.Int64
	maxLatency atomic.Int64 // nanoseconds from push to apply
}

// observe records one applied command pushed at at.
func (c *commandStats) observe(at time.Time, now time.Time) {
	c.applied.Add(1)
	d := int64(now.Sub(at))
	for {
		old := c.maxLatency.Load()
		if d <= old || c.maxLatency.CompareAndSwap(old, d) {
			return
		}
	}
}

// take returns the applied count and the largest latency, and resets both.
func (c *commandStats) take() (applied int64, maxLatency time.Duration) {
	return c.applied.Swap(0), time.Duration(c.maxLatency.Swap(0))
}

// commandStatsInterval is how often logCommandStats reports.
const commandStatsInterval = 10 * time.Second

// logCommandStats reports command path latency every interval until ctx or the
// display ends, so a live log shows whether the display goroutine delays input.
// Only a period with every counter at zero logs nothing: a stalled display
// applies nothing but still fills the ring.
func (s *Server) logCommandStats(ctx context.Context) {
	t := time.NewTicker(commandStatsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case <-t.C:
			s.reportCommandStats()
		}
	}
}

func (s *Server) reportCommandStats() {
	rs := s.commands.takeStats()
	applied, latency := s.commandStats.take()
	if applied == 0 && latency == 0 && rs == (ringStats{}) {
		return
	}
	s.log.Info().
		Int64("commands", applied).
		Int("merged", rs.merged).
		Int("max_queued", rs.maxLen).
		Float64("max_push_block_ms", float64(rs.maxPushBlock)/float64(time.Millisecond)).
		Float64("max_latency_ms", float64(latency)/float64(time.Millisecond)).
		Msg("command stats")
}

// applyQueued runs on the display goroutine; it stops applying once the
// server context is done, like the per-command check of the old batch.
func (s *Server) applyQueued(q queuedCommand) {
	if s.ctx != nil && s.ctx.Err() != nil {
		return
	}
	s.commandStats.observe(q.at, time.Now())
	s.apply(q.command())
}
