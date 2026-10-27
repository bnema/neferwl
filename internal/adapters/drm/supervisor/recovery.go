package supervisor

import (
	"errors"
	"maps"
	"slices"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

//hexcheck:ignore suspicious-business-logic-in-adapter preserves fail-closed lifetime retirement and native recovery ordering on the supervisor goroutine
func (run *outputsRun) onStopped(stopped routing.Stopped) (error, bool) {
	if !run.set.StoppedCurrent(stopped) {
		return nil, false
	}
	name := stopped.Name
	output := run.outputs[name]
	err := run.set.Finish(name)
	// finish observes done after the deferred Close. Only affirmative
	// terminal KMS inactivity retires this exact lifetime; renderer
	// errors and missing connector inventory are not physical proof.
	// A card whose node is gone was unplugged: nothing can still scan
	// out, so its outputs are retired even without KMS confirmation.
	// A removed card with its node present (driver unbind) stays
	// fail-closed.
	owner := run.cards[name]
	unplugged := owner != nil && owner.removed && !deviceNodeExists(owner.Path())
	if output != nil {
		for _, action := range run.policy.OnOutputStopped(stopped.Instance, output.InactiveOnClose(), owner != nil && owner.removed, !unplugged) {
			if retire, ok := action.(core.ActRetireInstance); ok {
				if !output.InactiveOnClose() {
					run.log.Info().Str("card", owner.Path()).Str("connector", name).Msg("retiring output of unplugged GPU")
				}
				run.set.SecurityEvent(ports.SecurityOutputRemoved{Instance: retire.Instance})
			}
		}
	}
	delete(run.outputs, name)
	if run.ctx.Err() != nil {
		return errors.Join(err, run.set.Wait()), true
	}
	if run.policy.OnOutputFailure(err) {
		// The backend currently owns one shared render device. Cancel every
		// consumer first; normal stopped handling joins and retires each lifetime.
		for n, r := range run.set.Outs {
			run.stopping[n] = false
			r.Stop()
		}
	}
	restart := run.stopping[name]
	delete(run.stopping, name)
	run.curs.Set(name, nil)
	card := run.cards[name]
	if card.removed && err != nil {
		// An unplugged card fails its outputs: that is a stop, not a fault.
		run.log.Info().Err(err).Str("connector", name).Msg("output of removed GPU stopped")
		err = nil
	}
	run.stopLocalDeviceUsers(card, name, err)
	card.Release(name)
	run.lastHeads[card] = releasedHead(run.lastHeads[card], name)
	delete(run.cards, name)
	if card.localLost && !slices.Contains(slices.Collect(maps.Values(run.cards)), card) {
		card.local.close()
		card.localLost = false
	}
	if td := run.b.dying[card]; td != nil && td.OutputStopped(name) {
		run.finishCard(card)
	}
	source := run.readySources[name]
	delete(run.readySources, name)
	if stopped := run.stopReady[name]; stopped != nil {
		close(stopped)
		delete(run.stopReady, name)
	}
	stopDecision := run.progress.Stopped(name, source, restart, err)
	if run.policy.Recovering {
		run.complete(stopDecision)
		run.send(ports.OutputRemoved{Name: name})
		if len(run.set.Outs) > 0 {
			return nil, false
		}
		old := run.b.render
		run.b.render = nil // failure cleanup must not close the lost device again.
		run.log.Warn().Str("card", run.b.renderInfo.Path).Msg("render device lost, reopening")
		next, reopenErr := reopenRender(run.b.opener, run.b.renderInfo, old, &run.policy.ReopenLimiter)
		if reopenErr != nil {
			return errors.Join(reopenErr, run.set.Wait()), true
		}
		run.b.render = next
		if lost := core.LostFormats(run.b.advertised, next.DMABuf().Formats); lost > 0 {
			run.log.Warn().Str("card", run.b.renderInfo.Path).Int("lost_formats", lost).Msg("render device lost advertised formats")
		}
		for _, c := range run.b.cards {
			c.SetRender(next)
		}
		for _, name := range run.policy.OnReopened() {
			run.addCard(name)
		}
		run.scan()
		return nil, false
	}

	if err != nil {
		// A broken output (e.g. its renderer) must not take the
		// session down. It stays off until the next hotplug, so a
		// lasting failure does not loop.
		run.log.Error().Err(err).Str("connector", name).Msg("output stopped")
	}
	restart = run.policy.RestartOutput(restart, err)
	// Release changes the enabled state in ConnectedHeads. Do not
	// restart a failed or disabled output just to refresh inventory.
	if restart {
		run.scan()
	} else {
		run.publishInventory()
	}
	run.complete(stopDecision)
	if restart {
		// Same connector, new mode: core keeps its screen and
		// workspaces; OutputAdded updates the size.
		if run.set.Outs[name] != nil {
			running := map[string]domain.OutputInstance{}
			for n := range run.set.Outs {
				running[n] = run.readySources[n]
			}
			run.complete(run.progress.Scanned(running))
			return nil, false
		}
	}
	run.send(ports.OutputRemoved{Name: name})
	running := map[string]domain.OutputInstance{}
	for n := range run.set.Outs {
		running[n] = run.readySources[n]
	}
	run.complete(run.progress.Scanned(running))
	return nil, false
}

// stopLocalDeviceUsers starts card-wide teardown only for affirmative device
// loss. Ordinary copier capability/import refusals leave sibling users alone.
func (run *outputsRun) stopLocalDeviceUsers(card *drmCard, name string, err error) {
	if !drm.IsLocalDeviceError(err) || card.local == nil {
		return
	}
	card.localLost = true
	for n, c := range run.cards {
		if c == card && n != name && run.set.Outs[n] != nil {
			run.stopping[n] = false
			run.progress.Stopping(n)
			run.set.Outs[n].Stop()
		}
	}
}
