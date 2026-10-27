package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
)

// trySpawn is a bounded handoff, not execution admission. The launcher checks
// Security again before exec: the gate may change after this owner's check.
func (c *Core) trySpawn(ctx context.Context, req domain.SpawnRequest) bool {
	if ctx.Err() != nil || c.protectionRequested() {
		return false
	}
	req.Security = c.security
	select {
	case c.ch.Spawn <- req:
		return true
	default:
		return false
	}
}

// Startup has at most the configured number of pending commands. Initial
// ready sends are bounded; Run selects the remainder alongside owner events.
func (c *Core) pendingStartup() (chan<- domain.SpawnRequest, domain.SpawnRequest) {
	if len(c.startup) == 0 || c.protectionRequested() {
		return nil, domain.SpawnRequest{}
	}
	return c.ch.Spawn, domain.SpawnRequest{Security: c.security, Argv: c.startup[0]}
}

func (c *Core) spawnStartup(ctx context.Context) {
	for len(c.startup) > 0 {
		if !c.trySpawn(ctx, domain.SpawnRequest{Argv: c.startup[0]}) {
			return
		}
		c.startup = c.startup[1:]
	}
}
