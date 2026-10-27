package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (run *outputsRun) startReader(c *drmCard) {
	rctx, stop := context.WithCancel(run.ctx)
	done := make(chan struct{})
	c.stopReader, c.readerDone = stop, done
	run.readers.Go(func() {
		defer close(done)
		if err := safe("drm events", func() error { return c.ReadEvents(rctx) }); err != nil {
			select {
			case run.readerErr <- readerFailure{c, fmt.Errorf("%s: %w", c.Path(), err)}:
			default:
				run.log.Error().Err(err).Str("card", c.Path()).Msg("drm event reader failure dropped: error queue full")
			}
		}
	})
}

func (run *outputsRun) addCard(name domain.CardName) bool {
	c, action := run.b.hotplugAdd(run.ctx, name, run.want(run.currentConfig))
	run.policy.OnCardOpen(name, action)
	if c == nil {
		return false
	}
	c.SetFormats(run.ch.Formats)
	run.b.cards = append(run.b.cards, c)
	run.startReader(c)
	return true
}

func (run *outputsRun) finishCard(c *drmCard) {
	c.stopReader()
	<-c.readerDone
	c.Card.Close()
	if c.local != nil {
		c.local.close()
	}
	run.b.seat.CloseDevice(c.fd)
	delete(run.b.dying, c)
	delete(run.lastHeads, c)
	run.log.Info().Str("card", c.Path()).Msg("GPU removed")
	for _, action := range run.policy.OnCardClosed(domain.CardName(filepath.Base(c.Path()))) {
		if open, ok := action.(core.ActOpenCard); ok {
			run.addCard(open.Card)
		}
	}
	// A same-name connector on another card was refused while this one
	// lived; the loop rescans (never inside a stop handler).
	select {
	case run.hotplug <- domain.HotplugEvent{}:
	default:
	}
}

//hexcheck:ignore suspicious-business-logic-in-adapter executes ordered native card teardown; card selection and teardown progress belong to core policy
func (run *outputsRun) removeCard(name domain.CardName) {
	idx := slices.IndexFunc(run.b.cards, func(c *drmCard) bool { return domain.CardName(filepath.Base(c.Path())) == name })
	if idx < 0 {
		return
	}
	c := run.b.cards[idx]
	c.removed = true
	run.b.cards = slices.Delete(run.b.cards, idx, idx+1)
	run.log.Info().Str("card", c.Path()).Msg("GPU unplugged")
	// A lease of a dead card is finished whether or not the revoke worked.
	for _, id := range c.LeaseIDs() {
		if err := c.Revoke(id); err != nil {
			run.log.Debug().Err(err).Str("card", c.Path()).Uint32("lease", id).Msg("revoke lease of removed GPU")
		}
		run.leases.Send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
	}
	run.leases.Forget(c)
	var names []string
	for n, owner := range run.cards {
		if owner == c && run.set.Outs[n] != nil {
			names = append(names, n)
		}
	}
	td := core.NewCardTeardown(names)
	if run.b.dying == nil {
		run.b.dying = map[*drmCard]*core.CardTeardown{}
	}
	run.b.dying[c] = td
	for _, n := range names {
		run.stopping[n] = false
		run.progress.Stopping(n)
		run.set.Outs[n].Stop()
	}
	run.publishInventory()
	if td.Done() {
		run.finishCard(c)
	}
}

func (run *outputsRun) onHotplug(ev domain.HotplugEvent) (error, bool) {
	scanRequested := false
	for _, action := range run.policy.OnHotplug(ev) {
		switch a := action.(type) {
		case core.ActExit:
			path := run.b.renderInfo.Path
			run.log.Error().Str("card", path).Msg("render GPU removed")
			return errors.Join(fmt.Errorf("%w: %s", a.Err, path), run.set.Wait()), true
		case core.ActOpenCard:
			if !run.addCard(a.Card) {
				goto hotplugDone
			}
		case core.ActRemoveCard:
			run.removeCard(a.Card)
		case core.ActScan:
			scanRequested = true
		}
	}
hotplugDone:
	if !scanRequested {
		return nil, false
	}
	run.log.Info().Str("card", string(ev.Card)).Msg("hotplug")
	run.scan()
	if run.progress.Active {
		running := map[string]domain.OutputInstance{}
		for name := range run.set.Outs {
			running[name] = run.readySources[name]
		}
		run.complete(run.progress.Scanned(running))
	}

	return nil, false
}
