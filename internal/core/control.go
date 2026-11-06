package core

import (
	"context"
	"errors"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// maxControlWaits caps the pending control waits: a request that would add
// a wait past it is refused with busy before it runs. Requests that add no
// wait, and quit, always pass.
const maxControlWaits = 64

// waitsFull reports whether one more pending wait would exceed the cap.
func (c *Core) waitsFull() bool { return len(c.controlWaits)+len(c.outputWaits) >= maxControlWaits }

// addsWait reports whether a request with this completion waits past its
// own pass (waitControl queues it).
func addsWait(level domain.WaitLevel, until *domain.Until, shots []domain.ShotRequest) bool {
	return level > domain.WaitCommitted || until != nil || len(shots) > 0
}

// control runs on the owner, just like input.
func (c *Core) control(ctx context.Context, r ports.ControlRequest) error {
	switch r := r.(type) {
	case ports.ControlOutput:
		return c.controlOutput(ctx, r)
	case ports.ControlDo:
		return c.controlDo(ctx, r)
	case ports.ControlQuery:
		c.controlQuery(r)
	case ports.ControlInject:
		return c.controlInject(ctx, r)
	case ports.ControlCapture:
		return c.controlCapture(ctx, r)
	}
	return nil
}

func (c *Core) controlDo(ctx context.Context, r ports.ControlDo) error {
	if r.Caps&domain.CapAct == 0 {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
		return nil
	}
	spec, ok := domain.LookupCommand(r.Action)
	if !ok || (spec.Kind != domain.CommandAction && spec.Kind != domain.CommandActionArg) {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrUnknownCommand})
		return nil
	}
	quit := Action(r.Action) == ActionQuit
	// Protected input runs no binds (protectedInput); control mirrors
	// it, except quit, which session rm needs on a locked session.
	if !quit && c.protectionRequested() {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
		return nil
	}
	shots, code := c.controlShots(r.Shots, r.Caps)
	if code != "" {
		controlReply(r.Reply, ports.ControlReply{Err: code})
		return nil
	}
	if !quit && addsWait(r.Wait, r.Until, shots) && c.waitsFull() {
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBusy})
		return nil
	}
	before := c.state()
	baseline := c.sceneSeqs()
	c.bindCombo, c.bindSource = "", domain.BindSourceControl
	err := c.runBind(ctx, Action(r.Action))
	if quit {
		// There is no subsequent settle/present pass after ErrQuit.
		now := c.state()
		controlReply(r.Reply, ports.ControlReply{Done: domain.WaitCommitted, State: now, Diff: domain.DiffState(before, now)})
		return err
	}
	c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply, shots)
	return err
}

func (c *Core) controlQuery(r ports.ControlQuery) {
	switch {
	case r.Caps&domain.CapRead == 0:
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrForbidden})
	case r.What != "state":
		controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrBadRequest})
	default:
		// State contains no window titles, including while protected.
		controlReply(r.Reply, ports.ControlReply{State: c.state(), Done: domain.WaitCommitted})
	}
}

func (c *Core) controlInject(ctx context.Context, r ports.ControlInject) error {
	refuse := func(code string) {
		controlAdmission(r.Admission, code)
		controlReply(r.Reply, ports.ControlReply{Err: code})
	}
	if r.Caps&domain.CapInput == 0 {
		refuse(ports.ControlErrForbidden)
		return nil
	}
	if c.protectionRequested() {
		refuse(ports.ControlErrLocked)
		return nil
	}
	shots, code := c.controlShots(r.Shots, r.Caps)
	if code != "" {
		refuse(code)
		return nil
	}
	if addsWait(r.Wait, r.Until, shots) && c.waitsFull() {
		refuse(ports.ControlErrBusy)
		return nil
	}
	controlAdmission(r.Admission, "")
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
		if c.protectionRequested() {
			controlReply(r.Reply, ports.ControlReply{Err: ports.ControlErrLocked})
			return nil
		}
	}
	c.waitControl(before, baseline, r.Wait, r.Until, r.Timeout, r.Reply, shots)
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
	if c.protectionRequested() {
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

func controlAdmission(ch chan<- ports.ControlAdmission, code string) {
	select {
	case ch <- ports.ControlAdmission{Err: code}:
	default:
	}
}
