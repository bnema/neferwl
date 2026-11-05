package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// control runs on the owner, just like input.
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
		baseline := c.sceneSeqs()
		c.bindCombo, c.bindSource = "", domain.BindSourceControl
		err := c.runBind(ctx, Action(r.Action))
		c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply)
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
		if r.Caps&domain.CapInput == 0 {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
			return nil
		}
		if c.security.Protected {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
			return nil
		}
		before, baseline := c.state(), c.sceneSeqs()
		for _, ev := range r.Events {
			if err := c.inputEvent(ctx, ev); err != nil {
				return err
			}
			// An epoch transition during a batch must not route later events
			// to lock surfaces, even if their stamps match the new epoch.
			if c.security.Protected || c.protectionRequested() {
				controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
				return nil
			}
		}
		c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply)
	}
	return nil
}

func controlReply(ch chan<- ports.ControlReply, reply ports.ControlReply) {
	select {
	case ch <- reply:
	default:
	}
}
