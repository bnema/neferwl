package core

import (
	"errors"
	"fmt"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// LeaseGuard checks the security snapshots surrounding native lease creation.
// Callers must also check the initial snapshot before creating a lease. If this
// check fails after creation, the adapter closes the FD and revokes the lease
// before proceeding with a backend barrier. Any generation change invalidates
// the lease, including a complete protect/unprotect cycle during acquisition.
func LeaseGuard(before, after domain.SecurityState) error {
	if before.Protected {
		return errors.New("session protected")
	}
	if after.Protected || after.Generation != before.Generation {
		return errors.New("session transition during lease")
	}
	return nil
}

// OutputAction is a synchronous instruction to the backend supervisor. Only
// core defines actions; execution never sends an event through core's owner.
type OutputAction interface{ outputAction() }

type ActExit struct{ Err error }
type ActRemoveCard struct{ Card domain.CardName }
type ActOpenCard struct{ Card domain.CardName }
type ActScan struct{}

func (ActExit) outputAction()       {}
func (ActRemoveCard) outputAction() {}
func (ActOpenCard) outputAction()   {}
func (ActScan) outputAction()       {}

// OutputPolicy owns decisions on the supervisor goroutine, not core's owner
// goroutine. Inputs contain resolved hardware facts rather than device handles.
type OutputPolicy struct {
	ReopenLimiter   ReopenLimiter
	RenderCard      domain.CardName
	Recovering      bool
	pending, replug PendingCards
	revokeAttempts  int
}

func (p *OutputPolicy) OnHotplug(ev domain.HotplugEvent) []OutputAction {
	switch ClassifyHotplug(ev, p.RenderCard) {
	case HotplugRenderRemoved:
		return []OutputAction{ActExit{Err: ErrRenderGPURemoved}}
	case HotplugCardAdded:
		if p.Recovering {
			p.pending.Add(ev.Card)
			return nil
		}
		return []OutputAction{ActOpenCard{Card: ev.Card}, ActScan{}}
	case HotplugCardRemoved:
		p.pending.Remove(ev.Card)
		p.replug.Remove(ev.Card)
		return []OutputAction{ActRemoveCard{Card: ev.Card}}
	default:
		return []OutputAction{ActScan{}}
	}
}

func (p *OutputPolicy) OnCardOpen(card domain.CardName, result HotplugAddAction) {
	if result.RetryAfterTeardown() {
		p.replug.Add(card)
	}
}

func (p *OutputPolicy) OnCardClosed(card domain.CardName) []OutputAction {
	if !p.replug.Has(card) {
		return nil
	}
	p.replug.Remove(card)
	if p.Recovering {
		p.pending.Add(card)
		return nil
	}
	return []OutputAction{ActOpenCard{Card: card}}
}

func (p *OutputPolicy) PendingCards() []domain.CardName { return p.pending.Take() }

// OnReaderFailure receives ENODEV and device-node existence as resolved facts:
// core does not depend on platform errno values or filesystem probes.
func (p *OutputPolicy) OnReaderFailure(card domain.CardName, removed, nodeExists, noDevice bool, err error) []OutputAction {
	if removed {
		return nil
	}
	if card == p.RenderCard {
		if !nodeExists || noDevice {
			return []OutputAction{ActExit{Err: errors.Join(ErrRenderGPURemoved, err)}}
		}
	} else if noDevice {
		return []OutputAction{ActRemoveCard{Card: card}}
	}
	return []OutputAction{ActExit{Err: err}}
}

// LeaseDecision is evaluated before entering the native lease operation. The
// gate snapshot is separate from forwarded state because forwarding can lag.
type LeaseDecision struct{ Err error }

func (p *OutputPolicy) OnLeaseRequest(seatActive bool, forwarded, gate domain.SecurityState) LeaseDecision {
	if !seatActive || forwarded.Protected || gate.Protected {
		return LeaseDecision{Err: errors.New("DRM seat inactive or session protected")}
	}
	return LeaseDecision{}
}

// OnSecurity starts a fresh retry budget only when forwarding changed state.
func (p *OutputPolicy) OnSecurity(changed bool) {
	if changed {
		p.revokeAttempts = 0
	}
}

// OnBarrier counts only completed barrier rounds, not rounds superseded by a
// newer gate generation. The adapter owns the one-second timer.
func (p *OutputPolicy) OnBarrier(err error) bool {
	p.revokeAttempts++
	return err != nil && p.revokeAttempts < 3
}

// OnOutputFailure begins shared-device recovery once. The caller stops all
// consumers before closing or reopening their render device.
func (p *OutputPolicy) OnOutputFailure(err error) bool {
	if p.Recovering || !errors.Is(err, ports.ErrDeviceLost) {
		return false
	}
	p.Recovering = true
	return true
}

func (p *OutputPolicy) OnReopened() []domain.CardName {
	p.Recovering = false
	return p.pending.Take()
}

// RestartOutput never loops a failed renderer, even if a mode change had
// already requested a restart. Another hotplug may try it again.
func (p *OutputPolicy) RestartOutput(requested bool, err error) bool {
	return requested && err == nil
}

type ActRetireInstance struct{ Instance domain.OutputInstance }

func (ActRetireInstance) outputAction() {}

func (p *OutputPolicy) OnOutputStopped(instance domain.OutputInstance, inactive, removed, nodeExists bool) []OutputAction {
	if RetireOnStop(inactive, removed, nodeExists) {
		return []OutputAction{ActRetireInstance{Instance: instance}}
	}
	return nil
}

// RegisterDiscardedOutput shares the caller's authoritative instance counter;
// refused connector names still represent real physical security lifetimes.
func RegisterDiscardedOutput(next domain.OutputInstance) (domain.OutputInstance, error) {
	if next == ^domain.OutputInstance(0) {
		return 0, errors.New("output instance exhausted")
	}
	return next + 1, nil
}

func RetireDiscardedOutput(instance domain.OutputInstance, inactive bool) error {
	if !inactive {
		return fmt.Errorf("discarded output %d shutdown inactivity unconfirmed", instance)
	}
	return nil
}
