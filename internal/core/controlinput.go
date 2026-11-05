package core

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// inputEvent is the common owner path for native and control input. Even
// stale input is activity, but admission prevents it becoming client input.
func (c *Core) inputEvent(ctx context.Context, ev ports.InputEvent) error {
	if err := c.securityCheckpoint(ctx); err != nil {
		return err
	}
	if err := c.userActivity(ctx); err != nil {
		return err
	}
	// Commands can backpressure: recheck after activity, before admission/binds.
	if err := c.securityCheckpoint(ctx); err != nil {
		return err
	}
	ev, admitted := c.admitInput(ev)
	if !admitted {
		return nil
	}
	return c.handleInput(ctx, ev)
}
