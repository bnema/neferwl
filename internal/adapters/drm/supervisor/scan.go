package supervisor

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (run *outputsRun) want(cfg domain.Config) drm.Want {
	w := wantFromConfig(cfg)
	w.Sampled = run.b.render.DMABuf().Formats
	w.TraceFlips = logging.Enabled(run.ctx, "drm-flip")
	return w
}

//hexcheck:ignore suspicious-business-logic-in-adapter preserves native scan, lifetime registration and shutdown ordering before the security barrier
func (run *outputsRun) scan() {
	if run.policy.Recovering {
		return
	}
	var scanErrors []error
	for _, c := range run.b.cards {
		if c.localLost {
			continue // Existing local-device users are still joining.
		}
		w := run.want(run.currentConfig)
		w.Device = c.Device()
		c.SetWant(w)
		added, removed, replaced, err := c.Scan()
		if err != nil {
			run.log.Warn().Err(err).Str("card", c.Path()).Msg("scan connectors")
			scanErrors = append(scanErrors, fmt.Errorf("scan %s: %w", c.Path(), err))
			continue
		}
		run.leases.Publish(c)
		stop := func(name string, restart bool) {
			if r := run.set.Outs[name]; r != nil && run.cards[name] == c {
				if _, ok := run.stopping[name]; !ok {
					run.stopping[name] = restart
					run.progress.Stopping(name)
					r.Stop()
				}
			}
		}
		for _, name := range removed {
			run.log.Info().Str("connector", name).Msg("output unplugged")
			stop(name, false)
		}
		for _, name := range replaced {
			run.log.Info().Str("connector", name).Msg("output mode change")
			stop(name, true)
		}
		for _, o := range added {
			// Every physical lifetime gets the gate before any early exit:
			// Close must not restore saved desktop, even on a name collision.
			o.Security, o.Frames = run.ch.Security, run.ch.Frames
			name := o.Info().Name
			if other := run.cards[name]; other != nil && other != c {
				// Core and clients know outputs by connector name.
				run.log.Warn().Str("connector", name).Str("card", c.Path()).Msg("connector name already used by another card; ignored")
				instance, registerErr := registerDiscardedOutput(run.set, name)
				o.Instance = instance
				o.Close() // synchronous final KMS disable; never starts a renderer
				var shutdownErr error
				if registerErr == nil {
					shutdownErr = retireDiscardedOutput(run.set, instance, o.InactiveOnClose())
				}
				if err := errors.Join(registerErr, shutdownErr); err != nil {
					if run.discardedOutputErr == nil {
						run.discardedOutputErr = err
					}
					scanErrors = append(scanErrors, err)
					run.log.Error().Err(err).Str("connector", name).Str("card", c.Path()).Msg("discarded output protection unconfirmed")
					// An exhausted instance cannot represent an unsafe lifetime.
					// Stop the backend rather than ever acknowledge this inventory.
					if instance == 0 {
						run.cancel()
					}
				}
				c.Release(name)
				continue
			}
			run.cards[name] = c
			run.outputs[name] = o

			if cur := o.Cursor(); cur != nil {
				run.curs.Set(name, cur)
			}
			active := run.b.seat.Subscribe()
			realtime := run.currentConfig.Performance.Realtime
			started := run.set.Start(run.ctx, name, func(octx context.Context, feed routing.Feed) error {
				defer run.b.seat.Unsubscribe(active)
				o.Security, o.SecurityChanges, o.Instance, o.StartOff = run.ch.Security, feed.Security, feed.Instance, feed.StartOff
				return safe("output "+name, func() error {
					defer o.Close()
					run.b.addPreparer(o)
					defer run.b.removePreparer(o) // before Close: it handles no request any more
					// octx derives from the session: the probe ends with this
					// output or the session.
					run.b.addProbe(name, func() { o.Ping(octx) })
					defer run.b.removeProbe(name)
					if realtime {
						runtime.LockOSThread()
						// Keep the thread locked until this goroutine exits: an RT
						// thread must not return to Go's general-purpose pool.
						schedLog := logging.For(run.ctx, "sched")
						if err := sched.Realtime(schedLog); err != nil {
							schedLog.Warn().Err(err).Msg("output scheduling")
						}
					}
					return o.Run(octx, xcursor.LoadCursor, active, feed.Scenes, feed.Contents, feed.Returns, feed.Cursor, run.ch.Presented, feed.Captures, run.ch.Captured)
				})
			}, &o.SecurityEvents)
			if !started {
				run.b.seat.Unsubscribe(active)
				o.Close()
				c.Release(name)
				// Undo the bookkeeping above: no stopped event will come.
				delete(run.cards, name)
				delete(run.outputs, name)
				delete(run.readySources, name)
				delete(run.progress.Ready, name)
				delete(run.stopReady, name)
				run.curs.Set(name, nil)
				continue
			}
			run.readySources[name] = run.set.Outs[name].Instance
			if run.progress.Active && !run.want(run.currentConfig).Disabled[name] {
				run.progress.Required[name] = true
			}
			source := run.readySources[name]
			stopped := make(chan struct{})
			run.stopReady[name] = stopped
			go func() {
				select {
				case err := <-o.Ready():
					select {
					case run.ready <- readyResult{name, source, err}:
					case <-run.ctx.Done():
					}
				case <-stopped:
				case <-run.ctx.Done():
				}
			}()
			run.progress.Started(name, source)
			run.send(ports.OutputAdded{Info: o.Info()})
		}
	}
	run.publishInventory()
	run.complete(run.progress.ScanError(errors.Join(scanErrors...)))
	run.barrier()
}

//hexcheck:ignore suspicious-business-logic-in-adapter Reads connected hardware heads and forwards the inventory to core policy.
func (run *outputsRun) publishInventory() {
	var inventory domain.OutputHeads
	for _, card := range run.b.cards {
		found, err := card.ConnectedHeads()
		if err != nil {
			run.log.Warn().Err(err).Str("card", card.Path()).Msg("inventory outputs")
		}
		run.lastHeads[card] = retainedHeads(run.lastHeads[card], found, err)
		inventory.Heads = append(inventory.Heads, run.lastHeads[card]...)
	}
	run.currentHeads = inventory
	run.apply.Heads(inventory)
}
