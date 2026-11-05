package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// control runs on the owner, just like input. Completion currently means only
// committed, irrespective of the requested wait level.
func (c *Core) control(ctx context.Context, r ports.ControlRequest) error {
	switch r := r.(type) {
	case ports.ControlDo:
		if r.Caps&domain.CapAct == 0 {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
			return nil
		}
		spec, ok := domain.LookupCommand(r.Action)
		if !ok || (spec.Kind != domain.CommandAction && spec.Kind != domain.CommandActionArg) {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrUnknownCommand})
			return nil
		}
		before := c.state()
		c.bindCombo, c.bindSource = "", domain.BindSourceControl
		err := c.runBind(ctx, Action(r.Action))
		after := c.state()
		controlReply(r.Reply, ports.ControlReply{State: after, Diff: domain.DiffState(before, after), Done: domain.WaitCommitted})
		return err
	case ports.ControlQuery:
		if r.Caps&domain.CapRead == 0 {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
		} else if r.What != "state" {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
		} else {
			// State contains no window titles, including while protected.
			controlReply(r.Reply, ports.ControlReply{State: c.state(), Done: domain.WaitCommitted})
		}
	case ports.ControlInject:
		// Input injection is not implemented yet.
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
	}
	return nil
}

func controlReply(ch chan<- ports.ControlReply, reply ports.ControlReply) {
	select {
	case ch <- reply:
	default:
	}
}
