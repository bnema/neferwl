package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/seat"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

var errTimeout = errors.New("timeout")

// drmBackend is the seat and the cards with at least one usable output.
type drmBackend struct {
	seat  *seat.Seat
	cards []*drmCard
}

type drmCard struct {
	*drm.Card
	fd int
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
	b := &drmBackend{seat: s}
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
// hotplug. Core learns about outputs through events. It returns when ctx
// ends, after every output is closed.
func (b *drmBackend) runOutputs(ctx context.Context, want func(ports.Config) drm.Want, initial ports.Config, events chan<- ports.OutputEvent, scenes <-chan []ports.Scene, contents <-chan ports.SurfaceContent, cursorChanges <-chan ports.CursorChange, presented chan<- ports.OutputPresented, captures <-chan ports.CaptureRequest, captured chan<- ports.CaptureDone, formats chan<- ports.OutputFormats, heads chan<- ports.OutputHeads, report chan<- ports.OutputHeads, configs <-chan ports.Config, applied chan<- error, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var readers sync.WaitGroup
	readerErr := make(chan error, len(b.cards))
	for _, c := range b.cards {
		c.SetFormats(formats)
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
	set := newOutputSet(ctx, captured)
	cards := map[string]*drmCard{}
	send := func(ev ports.OutputEvent) {
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}
	// stopping holds outputs asked to stop: true when they restart with a
	// new mode (core keeps the screen), false when they are gone.
	stopping := map[string]bool{}
	currentConfig := initial
	var currentHeads ports.OutputHeads
	var pendingConfig *ports.Config
	var pendingWait map[string]bool
	var pendingErr error
	// Complete only when every affected output stopped/restarted and is
	// running; the backend owns all outputSet access.
	complete := func() {
		if pendingConfig == nil || len(pendingWait) > 0 {
			return
		}
		for _, head := range currentHeads.Heads {
			if want(currentConfig).Disabled[head.Info.Name] {
				continue
			}
			if set.outs[head.Info.Name] == nil {
				pendingErr = errors.Join(pendingErr, fmt.Errorf("%s failed to start", head.Info.Name))
			}
		}
		select {
		case applied <- pendingErr:
		case <-ctx.Done():
		}
		pendingConfig = nil
		pendingErr = nil
	}
	scan := func() {
		for _, c := range b.cards {
			w := want(currentConfig)
			w.Device = c.Device()
			c.SetWant(w)
			added, removed, replaced, err := c.Scan()
			if err != nil {
				log.Warn().Err(err).Msg("scan connectors")
				continue
			}
			stop := func(name string, restart bool) {
				if r := set.outs[name]; r != nil && cards[name] == c {
					if _, ok := stopping[name]; !ok {
						stopping[name] = restart
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
				if cur := o.Cursor(); cur != nil {
					curs.set(name, cur)
				}
				active := b.seat.Subscribe()
				set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest) error {
					defer b.seat.Unsubscribe(active)
					return safe("output "+name, func() error {
						defer o.Close()
						return o.Run(octx, newRenderer, loadCursor, active, sc, cc, cu, presented, cap, captured)
					})
				})
				send(ports.OutputAdded{Info: o.Info()})
			}
		}
		var inventory ports.OutputHeads
		for _, card := range b.cards {
			found, err := card.ConnectedHeads()
			if err != nil {
				log.Warn().Err(err).Str("card", card.Path()).Msg("inventory outputs")
				continue
			}
			inventory.Heads = append(inventory.Heads, found...)
		}
		currentHeads = inventory
		select {
		case heads <- inventory:
		case <-ctx.Done():
		}
		select {
		case report <- inventory:
		case <-ctx.Done():
		}
	}
	scan()
	if len(set.outs) == 0 {
		return errors.Join(errors.New("no connected display"), set.wait())
	}
	for {
		select {
		case <-ctx.Done():
			return set.wait()
		case err := <-readerErr:
			// No more flips on that card: its outputs would freeze.
			return errors.Join(err, set.wait())
		case <-hotplug:
			log.Info().Msg("hotplug")
			scan()
		case currentConfig = <-configs:
			pendingConfig = &currentConfig
			pendingWait = map[string]bool{}
			for name := range set.outs {
				pendingWait[name] = true
			}
			scan()
			// Only outputs asked to stop must finish before acknowledging.
			for name := range pendingWait {
				if _, ok := stopping[name]; !ok {
					delete(pendingWait, name)
				}
			}
			complete()
		case s := <-scenes:
			set.scenes(s)
		case c := <-contents:
			set.content(c)
		case q := <-captures:
			set.routeCapture(q)
		case c := <-cursorChanges:
			set.setCursor(c)
		case name := <-set.stopped:
			err := set.finish(name)
			if ctx.Err() != nil {
				return errors.Join(err, set.wait())
			}
			restart := stopping[name]
			delete(stopping, name)
			curs.set(name, nil)
			cards[name].Release(name)
			delete(cards, name)
			if err != nil && pendingConfig != nil {
				pendingErr = errors.Join(pendingErr, err)
			}
			if err != nil {
				// A broken output (e.g. its renderer) must not take the
				// session down. It stays off until the next hotplug, so a
				// lasting failure does not loop.
				log.Error().Err(err).Str("connector", name).Msg("output stopped")
				restart = false
			}
			if restart {
				// Same connector, new mode: core keeps its screen and
				// workspaces; OutputAdded updates the size.
				scan()
				if set.outs[name] != nil {
					delete(pendingWait, name)
					complete()
					continue
				}
			}
			send(ports.OutputRemoved{Name: name})
			delete(pendingWait, name)
			complete()
		}
	}
}
