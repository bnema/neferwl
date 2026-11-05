package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// runtimeOutputTimeout bounds how long an output add or removal waits for the
// backend and then for the output to (dis)appear.
const runtimeOutputTimeout = domain.DefaultControlTimeout

func (c *Core) controlOutput(ctx context.Context, r ports.ControlOutput) error {
	if r.Caps&domain.CapAdmin == 0 {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
		return nil
	}
	if c.waitsFull() {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBusy})
		return nil
	}
	if c.runtimeWake == nil {
		c.runtimeWake = make(chan struct{}, 1)
	}
	result := make(chan ports.OutputRuntimeResult, 1)
	request := ports.OutputRuntimeRequest{Name: r.Name, Mode: r.Mode, Remove: r.Remove, Reply: result, Wake: c.runtimeWake}
	select {
	case c.ch.RuntimeOutputs <- request:
		c.outputWaits = append(c.outputWaits, outputWait{before: c.state(), runtime: result, remove: r.Remove, reply: r.Reply, deadline: c.now().Add(runtimeOutputTimeout)})
	case <-ctx.Done():
		return ctx.Err()
	default:
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
	}
	return nil
}
