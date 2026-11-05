package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (c *Core) controlOutput(ctx context.Context, r ports.ControlOutput) error {
	if r.Caps&domain.CapAdmin == 0 {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
		return nil
	}
	if c.runtimeWake == nil {
		c.runtimeWake = make(chan struct{}, 1)
	}
	result := make(chan ports.OutputRuntimeResult, 1)
	request := ports.OutputRuntimeRequest{Name: r.Name, Mode: r.Mode, Remove: r.Remove, Reply: result, Wake: c.runtimeWake}
	select {
	case c.ch.RuntimeOutputs <- request:
		c.controlWaits = append(c.controlWaits, controlWait{before: c.state(), runtime: result, remove: r.Remove, reply: r.Reply})
	case <-ctx.Done():
		return ctx.Err()
	default:
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
	}
	return nil
}
