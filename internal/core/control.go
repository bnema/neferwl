package core

import (
	"context"
	"errors"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// maxControlWaits caps the pending control waits: a request that would wait
// past it is refused before it runs.
const maxControlWaits = 64

// control runs on the owner, just like input.
func (c *Core) control(ctx context.Context, r ports.ControlRequest) error {
	if len(c.controlWaits) >= maxControlWaits {
		switch r := r.(type) {
		case ports.ControlOutput:
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
			return nil
		case ports.ControlDo:
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
			return nil
		case ports.ControlInject:
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
			return nil
		}
	}
	switch r := r.(type) {
	case ports.ControlOutput:
		return c.controlOutput(ctx, r)
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
		// Protected input runs no binds (protectedInput); control mirrors
		// it, except quit, which session rm needs on a locked session.
		if Action(r.Action) != ActionQuit && c.protectionRequested() {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
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
				// The batch stops here: answer it so the client does not hang.
				if errors.Is(err, errSecurityChanged) || c.protectionRequested() {
					controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
				} else {
					now := c.state()
					controlReply(r.Reply, ports.ControlReply{Done: domain.WaitCommitted, State: now, Diff: domain.DiffState(before, now)})
				}
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
	var shots []domain.ShotRequest
	for _, q := range requests {
		found := false
		// The screens in state order, without building the state.
		for _, sc := range c.screens {
			if name := sc.name(); name != "" && (q.Output == "" || q.Output == name) {
				shots = append(shots, domain.ShotRequest{Output: name, Raw: q.Raw})
				found = true
			}
		}
		if !found {
			return nil, ports.ControlErrBadRequest
		}
	}
	return shots, ""
}
