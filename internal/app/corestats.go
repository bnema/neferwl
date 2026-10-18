package app

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/zerowrap"
)

// commandStatsEvery is how often the command outbox counters are logged,
// like the output and input stats.
const commandStatsEvery = 10 * time.Second

// logCommandStats logs core's command counters every commandStatsEvery
// until ctx ends. A queued command or a flush wait means the display goroutine
// held core's commands up; an idle period logs nothing.
func logCommandStats(ctx context.Context, c *core.Core, log zerowrap.Logger) {
	t := time.NewTicker(commandStatsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := c.CommandStats()
		if s.Direct == 0 && s.Queued == 0 {
			continue
		}
		log.Info().Int("direct", s.Direct).Int("queued", s.Queued).Int("merged", s.Merged).
			Uint64("max_backlog", s.MaxBacklog).
			Float64("max_queue_wait_ms", s.MaxQueueWait.Seconds()*1e3).
			Float64("max_flush_wait_ms", s.MaxFlushWait.Seconds()*1e3).Msg("outbox stats")
	}
}
