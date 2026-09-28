package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/ddc"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/seat"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

var errTimeout = errors.New("timeout")

// applyTimeout bounds how long outputs take to apply a configuration.
const applyTimeout = 5 * time.Second

// drmBackend is the seat and the cards with at least one usable output.
type drmBackend struct {
	seat  *seat.Seat
	cards []*drmCard
	clock ports.Clock
}

type drmCard struct {
	*drm.Card
	fd int
}

// A failed card read must not erase its heads from a multi-card inventory.
func retainedHeads(previous, found []ports.OutputHead, err error) []ports.OutputHead {
	if err != nil {
		return previous
	}
	return found
}

// A stopped instance is no longer enabled, even if the inventory read fails.
func releasedHead(heads []ports.OutputHead, name string) []ports.OutputHead {
	heads = append([]ports.OutputHead(nil), heads...)
	for i := range heads {
		if heads[i].Info.Name == name {
			heads[i].Enabled = false
			heads[i].Current = nil
		}
	}
	return heads
}

// safe turns a panic in a hardware goroutine into an error, so the TTY is still restored.
func safe(name string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panic: %v\n%s", name, p, debug.Stack())
		}
	}()
	return fn()
}

// openDRM opens the seat and every card with a connected display. Outputs
// are found by the first Scan of each card.
func openDRM(ctx context.Context, cfg ports.Config) (*drmBackend, error) {
	log := logging.For(ctx, "drm")
	s, err := seat.Open(ctx, logging.For(ctx, "seat"))
	if err != nil {
		log.Error().Err(err).Msg("open seat")
		return nil, fmt.Errorf("open seat: %w", err)
	}
	want := wantFromConfig(cfg)
	paths, _ := filepath.Glob("/dev/dri/card[0-9]*")
	sort.Strings(paths)
	b := &drmBackend{seat: s, clock: clock.System{}}
	var errs []error
	for _, path := range paths {
		fd, err := s.OpenDevice(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		card, err := drm.OpenCard(fd, path, want, log)
		if err != nil {
			log.Warn().Err(err).Str("card", path).Msg("card unusable")
			errs = append(errs, err)
			s.CloseDevice(fd)
			continue
		}
		b.cards = append(b.cards, &drmCard{Card: card, fd: fd})
	}
	if len(b.cards) == 0 {
		s.Close()
		err = fmt.Errorf("no usable DRM card in %v: %w", paths, errors.Join(errs...))
		log.Error().Err(err).Msg("open drm")
		return nil, err
	}
	return b, nil
}

// wantFromConfig turns output.<name> and render.* entries into connector choices.
func wantFromConfig(cfg ports.Config) drm.Want {
	r := cfg.Render
	want := drm.Want{Disabled: map[string]bool{}, Modes: map[string][3]float64{}, HDR: map[string]drm.HDRSettings{}, NoScanout: !r.DirectScanout, NoTearing: !r.Tearing, NoVRR: !r.VRR}
	for _, o := range cfg.Outputs {
		nits := o.SDRBrightness
		if nits == 0 {
			nits = ports.DefaultSDRBrightness
		}
		want.HDR[o.Name] = drm.HDRSettings{Enabled: o.HDR, SDRBrightness: nits}
		if o.Off {
			want.Disabled[o.Name] = true
			continue
		}
		if o.Mode != "" {
			// Validated at config load.
			w, h, hz, _ := config.ParseMode(o.Mode)
			want.Modes[o.Name] = [3]float64{float64(w), float64(h), hz}
		}
	}
	return want
}

// close releases the cards and the seat so the TTY comes back. Outputs are
// closed by their goroutines first.
func (b *drmBackend) close() {
	for _, c := range b.cards {
		b.seat.CloseDevice(c.fd)
	}
	b.seat.Close()
}

// runOutputs drives every output of every card: one goroutine per output,
// one flip reader per card, and a udev watcher that rescans connectors on
// hotplug. Core learns about outputs through events. The loop also owns
// output configuration: reloads and protocol requests go through apply, and
// each configuration is finished when its outputs are ready, fail or time
// out. It returns when ctx ends, after every output is closed.
func (b *drmBackend) runOutputs(ctx context.Context, want func(ports.Config) drm.Want, initial ports.Config, apply *outputApply, ch outputChannels, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var readers sync.WaitGroup
	readerErr := make(chan error, len(b.cards))
	for _, c := range b.cards {
		c.SetFormats(ch.formats)
		readers.Go(func() {
			if err := safe("drm events", func() error { return c.ReadEvents(ctx) }); err != nil {
				readerErr <- fmt.Errorf("%s: %w", c.Path(), err)
			}
		})
	}
	defer readers.Wait()
	hotplug := make(chan struct{}, 1)
	go func() {
		if err := safe("hotplug", func() error { return drm.WatchHotplug(ctx, hotplug) }); err != nil {
			log.Warn().Err(err).Msg("hotplug watch disabled")
		}
	}()
	set := newOutputSet(ctx, ch.captured)
	cards := map[string]*drmCard{}
	// watchers stop the input source watch of each connected output. A
	// watch reports through inputs with its generation: a report of a
	// stopped watch, still in flight, is dropped, so core never hears of an
	// old monitor after OutputRemoved or a new OutputAdded.
	type watch struct {
		gen  uint64
		stop context.CancelFunc
	}
	type inputReport struct {
		gen uint64
		ev  ports.OutputInput
	}
	watchers := map[string]watch{}
	inputs := make(chan inputReport)
	var watchGen uint64
	defer func() {
		for _, w := range watchers {
			w.stop()
		}
	}()
	send := func(ev ports.OutputEvent) {
		select {
		case ch.events <- ev:
		case <-ctx.Done():
		}
	}
	// stopping holds outputs asked to stop: true when they restart with a
	// new mode (core keeps the screen), false when they are gone.
	stopping := map[string]bool{}
	currentConfig := initial
	var currentHeads ports.OutputHeads
	lastHeads := map[*drmCard][]ports.OutputHead{}
	progress := &applyProgress{completed: map[<-chan error]bool{}}
	readySources := map[string]<-chan error{}
	stopReady := map[string]chan struct{}{}
	type readyResult struct {
		name   string
		source <-chan error
		err    error
	}
	ready := make(chan readyResult)
	// deadline is the pending apply's timer channel, nil while none is pending.
	timer := b.clock.NewTimer(applyTimeout)
	timer.Stop()
	defer timer.Stop()
	var deadline <-chan time.Time
	var op uint64
	complete := func(d applyDecision) {
		if !d.reply {
			return
		}
		timer.Stop()
		deadline = nil
		apply.finished(d.err)
	}
	publishInventory := func() {
		var inventory ports.OutputHeads
		for _, card := range b.cards {
			found, err := card.ConnectedHeads()
			if err != nil {
				log.Warn().Err(err).Str("card", card.Path()).Msg("inventory outputs")
			}
			lastHeads[card] = retainedHeads(lastHeads[card], found, err)
			inventory.Heads = append(inventory.Heads, lastHeads[card]...)
		}
		currentHeads = inventory
		apply.heads(inventory)
	}
	scan := func() {
		var scanErrors []error
		for _, c := range b.cards {
			w := want(currentConfig)
			w.Device = c.Device()
			c.SetWant(w)
			added, removed, replaced, err := c.Scan()
			if err != nil {
				log.Warn().Err(err).Str("card", c.Path()).Msg("scan connectors")
				scanErrors = append(scanErrors, fmt.Errorf("scan %s: %w", c.Path(), err))
				continue
			}
			stop := func(name string, restart bool) {
				if r := set.outs[name]; r != nil && cards[name] == c {
					if _, ok := stopping[name]; !ok {
						stopping[name] = restart
						progress.stopping(name)
						r.stop()
					}
				}
			}
			for _, name := range removed {
				log.Info().Str("connector", name).Msg("output unplugged")
				stop(name, false)
			}
			for _, name := range replaced {
				log.Info().Str("connector", name).Msg("output mode change")
				stop(name, true)
			}
			for _, o := range added {
				name := o.Info().Name
				if other := cards[name]; other != nil && other != c {
					// Core and clients know outputs by connector name.
					log.Warn().Str("connector", name).Str("card", c.Path()).Msg("connector name already used by another card; ignored")
					o.Close()
					c.Release(name)
					continue
				}
				cards[name] = c
				readySources[name] = o.Ready()
				if progress.active && !want(currentConfig).Disabled[name] {
					progress.required[name] = true
				}
				source := readySources[name]
				stopped := make(chan struct{})
				stopReady[name] = stopped
				go func() {
					select {
					case err := <-source:
						select {
						case ready <- readyResult{name, source, err}:
						case <-ctx.Done():
						}
					case <-stopped:
					case <-ctx.Done():
					}
				}()
				progress.started(name, source)
				if cur := o.Cursor(); cur != nil {
					curs.set(name, cur)
				}
				active := b.seat.Subscribe()
				set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest) error {
					defer b.seat.Unsubscribe(active)
					return safe("output "+name, func() error {
						defer o.Close()
						return o.Run(octx, newRenderer, loadCursor, active, sc, cc, cu, ch.presented, cap, ch.captured)
					})
				})
				send(ports.OutputAdded{Info: o.Info()})
				// One watch per connector: a mode change keeps it, so no
				// report of an older watch follows the new OutputAdded.
				if _, ok := watchers[name]; !ok {
					watchGen++
					gen := watchGen
					wctx, stopWatch := context.WithCancel(ctx)
					watchers[name] = watch{gen, stopWatch}
					report := func(ev ports.OutputInput) bool {
						select {
						case inputs <- inputReport{gen, ev}:
							return true
						case <-wctx.Done():
							return false
						}
					}
					go ddc.Watch(wctx, c.Path(), name, b.clock, report, logging.For(ctx, "ddc"))
				}
			}
		}
		publishInventory()
		complete(progress.scanError(errors.Join(scanErrors...)))
	}
	scan()
	if len(set.outs) == 0 {
		return errors.Join(errors.New("no connected display"), set.wait())
	}
	// configure starts cfg; complete reports its result to apply. apply
	// hands out one configuration at a time, and a rollback or reload starts
	// on a later iteration, never inside scan or another configure.
	configure := func(cfg ports.Config) {
		currentConfig = cfg
		op++
		timer.Stop()
		deadline = nil
		required := map[string]bool{}
		// Desired enabled heads, not the inventory's current mode, require a first modeset.
		w := want(currentConfig)
		for _, h := range currentHeads.Heads {
			if !w.Disabled[h.Info.Name] {
				required[h.Info.Name] = true
			}
		}
		for name := range set.outs {
			if !w.Disabled[name] {
				required[name] = true
			}
		}
		progress.start(op, required, stopping)
		scan()
		running := map[string]<-chan error{}
		for name := range set.outs {
			running[name] = readySources[name]
		}
		d := progress.scanned(running)
		if progress.active {
			timer.Reset(applyTimeout)
			deadline = timer.C()
		}
		complete(d)
	}
	for {
		if cfg, ok := apply.next(); ok {
			configure(cfg)
			continue
		}
		configs, configNext := apply.configOut(ch.configured), apply.config
		replies, replyNext := apply.replyOut(ch.replies)
		heads, headsNext := apply.headsOut(ch.heads)
		select {
		case <-ctx.Done():
			return set.wait()
		case err := <-readerErr:
			// No more flips on that card: its outputs would freeze.
			return errors.Join(err, set.wait())
		case <-hotplug:
			log.Info().Msg("hotplug")
			scan()
			if progress.active {
				running := map[string]<-chan error{}
				for name := range set.outs {
					running[name] = readySources[name]
				}
				complete(progress.scanned(running))
			}
		case ev := <-ch.reloads:
			apply.reload(ev.Config)
		case req := <-ch.requests:
			apply.request(req)
		case configs <- configNext:
			apply.configSent()
		case replies <- replyNext:
			apply.replySent()
		case heads <- headsNext:
			apply.headsSent()
		case r := <-inputs:
			if w, ok := watchers[r.ev.Name]; ok && w.gen == r.gen {
				send(r.ev)
			}
		case result := <-ready:
			complete(progress.readyEvent(result.name, result.source, readySources[result.name], result.err))
		case <-deadline:
			complete(progress.timeout(op))
		case s := <-ch.scenes:
			set.scenes(s)
		case c := <-ch.contents:
			set.content(c)
		case q := <-ch.captures:
			set.routeCapture(q)
		case c := <-ch.cursorChanges:
			set.setCursor(c)
		case name := <-set.stopped:
			err := set.finish(name)
			if ctx.Err() != nil {
				return errors.Join(err, set.wait())
			}
			restart := stopping[name]
			delete(stopping, name)
			curs.set(name, nil)
			card := cards[name]
			card.Release(name)
			lastHeads[card] = releasedHead(lastHeads[card], name)
			delete(cards, name)
			source := readySources[name]
			delete(readySources, name)
			if stopped := stopReady[name]; stopped != nil {
				close(stopped)
				delete(stopReady, name)
			}
			stopDecision := progress.stopped(name, source, restart, err)
			if err != nil {
				// A broken output (e.g. its renderer) must not take the
				// session down. It stays off until the next hotplug, so a
				// lasting failure does not loop.
				log.Error().Err(err).Str("connector", name).Msg("output stopped")
				restart = false
			}
			// Release changes the enabled state in ConnectedHeads. Do not
			// restart a failed or disabled output just to refresh inventory.
			if restart {
				scan()
			} else {
				publishInventory()
			}
			complete(stopDecision)
			if restart {
				// Same connector, new mode: core keeps its screen and
				// workspaces; OutputAdded updates the size.
				if set.outs[name] != nil {
					running := map[string]<-chan error{}
					for n := range set.outs {
						running[n] = readySources[n]
					}
					complete(progress.scanned(running))
					continue
				}
			}
			// Core ignores reports for outputs it does not know, so a
			// report racing OutputRemoved is harmless.
			if w, ok := watchers[name]; ok {
				w.stop()
				delete(watchers, name)
			}
			send(ports.OutputRemoved{Name: name})
			running := map[string]<-chan error{}
			for n := range set.outs {
				running[n] = readySources[n]
			}
			complete(progress.scanned(running))
		}
	}
}
