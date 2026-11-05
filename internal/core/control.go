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
		shots, code := c.controlShots(r.Shots, r.Caps)
		if code != "" {
			controlReply(r.Reply, ports.ControlReply{Err: code})
			return nil
		}
		before := c.state()
		baseline := c.sceneSeqs()
		c.bindCombo, c.bindSource = "", domain.BindSourceControl
		err := c.runBind(ctx, Action(r.Action))
		c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply, shots)
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
		shots, code := c.controlShots(r.Shots, r.Caps)
		if code != "" {
			controlReply(r.Reply, ports.ControlReply{Err: code})
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
		c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply, shots)
	}
	return nil
}

func controlReply(ch chan<- ports.ControlReply, reply ports.ControlReply) {
	select {
	case ch <- reply:
	default:
	}
}

// controlShots validates before execution and expands all-output selectors in
// snapshot order. Caller-supplied sequence values are never trusted.
func (c *Core) controlShots(requests []domain.ShotRequest, caps domain.Caps) ([]domain.ShotRequest, string) {
	if len(requests) == 0 {
		return nil, ""
	}
	if caps&domain.CapCapture == 0 {
		return nil, ports.ControlErrForbidden
	}
	if c.security.Protected || c.protectionRequested() {
		return nil, ports.ControlErrLocked
	}
	outputs := c.state().Outputs
	var shots []domain.ShotRequest
	for _, q := range requests {
		found := false
		for _, o := range outputs {
			if q.Output == "" || q.Output == o.Name {
				shots = append(shots, domain.ShotRequest{Output: o.Name, Raw: q.Raw})
				found = true
			}
		}
		if !found {
			return nil, ports.ControlErrBadRequest
		}
	}
	return shots, ""
}
