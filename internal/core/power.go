package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// userActivity tells wayland the user touched an input device, at most
// once per ports.ActivityInterval: idle notification timers restart.
// Input also forgets the outputs unplugged while off: the user is back, so
// they reconnect on.
func (c *Core) userActivity(ctx context.Context) error {
	clear(c.offGone)
	now := time.Now()
	if now.Sub(c.activity) < ports.ActivityInterval {
		return nil
	}
	c.activity = now
	return c.command(ctx, ports.UserActivity{})
}

// offOutputs lists the outputs turned off by a client, in screen order.
func (c *Core) offOutputs() []string {
	var off []string
	for _, s := range c.screens {
		if s.off && s.name() != "" {
			off = append(off, s.name())
		}
	}
	return off
}
