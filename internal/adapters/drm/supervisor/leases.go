package supervisor

import (
	"errors"
	"fmt"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (run *outputsRun) barrier() {
	if !run.set.State.Protected {
		return
	}
	var errs []error
	for _, c := range run.b.cards {
		errs = append(errs, revokeSecurityLeases(c, func(id uint32) {
			run.leases.Send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
		}))
		run.leases.Publish(c)
	}
	errs = append(errs, run.discardedOutputErr)
	err := errors.Join(errs...)
	// Registration runs synchronously in scan before any barrier. A gate
	// transition racing native revocation belongs to a later round.
	if securitySnapshot(run.ch.Security).Generation > run.set.State.Generation {
		return
	}
	run.set.SecurityEvent(ports.SecurityBackendBarrier{Generation: run.set.State.Generation, Err: err})
	if run.policy.OnBarrier(err) {
		run.revokeTimer.Reset(time.Second)
		run.revokeDeadline = run.revokeTimer.C()
	}
}

func (run *outputsRun) onSecurity(state domain.SecurityState, ok bool) (error, bool) {
	if !ok {
		run.ch.SecurityChanges = nil
		return nil, false
	}
	if state.Generation < run.set.State.Generation {
		return nil, false
	}
	run.policy.OnSecurity(run.set.SetSecurity(state))
	// start can refresh the gate while scanning; its queued transition
	// must still receive a barrier even if forwarding already occurred.
	run.revokeTimer.Stop()
	run.revokeDeadline = nil
	if run.set.State.Protected {
		run.barrier()
	} else {
		for _, c := range run.b.cards {
			run.leases.Publish(c)
		}
	}

	return nil, false
}

func (run *outputsRun) onSeat(enabled bool) (error, bool) {
	run.seatActive = enabled
	if !enabled {
		for _, c := range run.b.cards {
			for _, id := range c.LeaseIDs() {
				if err := c.Revoke(id); err != nil {
					run.log.Warn().Err(err).Uint32("lease", id).Msg("VT lease revoke")
				} else {
					run.leases.Send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
				}
			}
			run.leases.Publish(c)
		}
	} else {
		for _, c := range run.b.cards {
			run.leases.Publish(c)
		}
	}

	return nil, false
}

func (run *outputsRun) onLease(event ports.LeaseMessage) (error, bool) {
	switch req := event.(type) {
	case ports.LeaseRequest:
		reply := ports.LeaseReply{ID: req.ID, Err: fmt.Errorf("unknown card %s", req.Card)}
		if decision := run.policy.OnLeaseRequest(run.seatActive, run.set.State, securitySnapshot(run.ch.Security)); decision.Err != nil {
			reply.Err = decision.Err
			run.leases.Send(reply)
			break
		}
		for _, c := range run.b.cards {
			if c.Path() == req.Card {
				reply.FD, reply.LeaseID, reply.Err = guardedLease(c, run.ch.Security, req.Connectors)
				run.leases.Publish(c)
				break
			}
		}
		run.leases.Send(reply)
	case ports.LeaseRevoke:
		for _, c := range run.b.cards {
			if c.Path() == req.Card {
				if err := c.Revoke(req.LeaseID); err != nil {
					run.log.Warn().Err(err).Uint32("lease", req.LeaseID).Msg("revoke lease")
				} else {
					run.leases.Publish(c)
				}
				break
			}
		}
	}

	return nil, false
}
