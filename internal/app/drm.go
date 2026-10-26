package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/seat"
	"github.com/bnema/neferwl/internal/adapters/watchdog"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

var errTimeout = errors.New("timeout")

// securityLeaseCard keeps the native operation and its tracked lease inventory
// together. A failed revoke must remain in LeaseIDs for the next barrier attempt.
type securityLeaseCard interface {
	Path() string
	Lease([]string) (*os.File, uint32, error)
	LeaseIDs() []uint32
	Revoke(uint32) error
}

func securitySnapshot(gate ports.SessionSecurity) ports.SecurityState {
	if gate == nil {
		return ports.SecurityState{}
	}
	return gate.Snapshot()
}

// guardedLease denies requests pending at acquisition and closes any FD created
// across a transition, even when the native call succeeds. Failed revocations
// stay tracked by the card and prevent the later backend barrier.
func guardedLease(c securityLeaseCard, gate ports.SessionSecurity, connectors []string) (*os.File, uint32, error) {
	before := securitySnapshot(gate)
	if before.Protected {
		return nil, 0, errors.New("session protected")
	}
	fd, id, err := c.Lease(connectors)
	if err != nil {
		return fd, id, err
	}
	after := securitySnapshot(gate)
	if after.Protected || after.Generation != before.Generation {
		if fd != nil {
			fd.Close()
		}
		return nil, 0, errors.Join(errors.New("session transition during lease"), c.Revoke(id))
	}
	return fd, id, nil
}

func revokeSecurityLeases(c securityLeaseCard, finished func(uint32)) error {
	var errs []error
	for _, id := range c.LeaseIDs() {
		if err := c.Revoke(id); err != nil {
			errs = append(errs, fmt.Errorf("%s lease %d: %w", c.Path(), id, err))
		} else {
			finished(id)
		}
	}
	if ids := c.LeaseIDs(); len(ids) != 0 {
		errs = append(errs, fmt.Errorf("%s leases still active: %v", c.Path(), ids))
	}
	return errors.Join(errs...)
}

// registerDiscardedOutput accounts for physical output lifetimes that cannot
// enter connector-name routing. They still require protection just like owners.
// The same backend-owned counter is used by outputSet.start; no instance reuse.
func registerDiscardedOutput(set *outputSet, name string) (ports.OutputInstance, error) {
	if set.nextInstance == ^ports.OutputInstance(0) {
		set.startErr = errors.New("output instance exhausted")
		return 0, set.startErr
	}
	set.nextInstance++
	instance := set.nextInstance
	if !set.securityEvent(ports.SecurityOutputAdded{Instance: instance, Output: name}) {
		return instance, set.ctx.Err()
	}
	return instance, nil
}

// retireDiscardedOutput consumes only affirmative terminal evidence. A failed
// close remains in authoritative inventory, even though it has no render owner.
func retireDiscardedOutput(set *outputSet, instance ports.OutputInstance, inactive bool) error {
	if !inactive {
		return fmt.Errorf("discarded output %d shutdown inactivity unconfirmed", instance)
	}
	if !set.securityEvent(ports.SecurityOutputRemoved{Instance: instance}) {
		return set.ctx.Err()
	}
	return nil
}

// applyTimeout bounds how long outputs take to apply a configuration.
const applyTimeout = 5 * time.Second

// drmBackend is the seat and the cards with at least one usable output.
type drmBackend struct {
	render     ports.RenderDevice
	opener     ports.RenderDeviceOpener
	renderInfo ports.GPUInfo
	advertised []ports.DMABufFormat
	seat       *seat.Seat
	cards      []*drmCard
	// dying holds unplugged cards until their outputs are stopped. Only the
	// runOutputs goroutine touches it; close reads it after that returns.
	dying map[*drmCard]*cardTeardown
	clock ports.Clock
	log   zerowrap.Logger
	// preparers are the running outputs, guarded by mu: the seat's goroutine
	// reads them while the output loop adds and removes them.
	mu        sync.Mutex
	preparers map[seatDisablePreparer]struct{}
	// probes are the running output loops the watchdog pings, by
	// connector, guarded by mu.
	probes map[string]func()
}

func (b *drmBackend) addProbe(name string, ping func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probes == nil {
		b.probes = map[string]func(){}
	}
	b.probes[name] = ping
}

func (b *drmBackend) removeProbe(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.probes, name)
}

// watchdogProbes lists the running output loops for the watchdog.
func (b *drmBackend) watchdogProbes() []watchdog.Probe {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]watchdog.Probe, 0, len(b.probes))
	for name, ping := range b.probes {
		out = append(out, watchdog.Probe{Name: "output-" + name, Ping: ping})
	}
	return out
}

// seatDisablePreparer is an output that can reset its planes before the
// seat is disabled (drm.Output).
type seatDisablePreparer interface {
	PrepareSeatDisable(timeout time.Duration) bool
}

// seatDisableTimeout bounds how long a seat disable waits for the outputs:
// the VT switch must not hang on a stuck one.
const seatDisableTimeout = 100 * time.Millisecond

func (b *drmBackend) addPreparer(p seatDisablePreparer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.preparers == nil {
		b.preparers = map[seatDisablePreparer]struct{}{}
	}
	b.preparers[p] = struct{}{}
}

func (b *drmBackend) removePreparer(p seatDisablePreparer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.preparers, p)
}

// prepareSeatDisable runs on the seat's goroutine before the seat is
// disabled and its devices revoked: every running output commits what it
// must leave behind (planes on Bypass), all in parallel under one bound. A
// timeout is logged and the disable goes on.
func (b *drmBackend) prepareSeatDisable() {
	b.mu.Lock()
	outs := make([]seatDisablePreparer, 0, len(b.preparers))
	for p := range b.preparers {
		outs = append(outs, p)
	}
	b.mu.Unlock()
	var wg sync.WaitGroup
	var late atomic.Int32
	for _, p := range outs {
		wg.Go(func() {
			if !p.PrepareSeatDisable(seatDisableTimeout) {
				late.Add(1)
			}
		})
	}
	wg.Wait()
	if n := late.Load(); n > 0 {
		b.log.Warn().Int("outputs", int(n)).Dur("timeout", seatDisableTimeout).Msg("outputs did not prepare for the seat disable in time; disabling anyway")
	}
}

type drmCard struct {
	*drm.Card
	fd    int
	local *localRenderDevice
	// removed is set by the runOutputs goroutine when the card is unplugged;
	// its reader may then fail without ending the session.
	removed bool
	// stopReader and readerDone belong to the card's ReadEvents goroutine.
	stopReader context.CancelFunc
	readerDone <-chan struct{}
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
func openDRM(ctx context.Context, cfg ports.Config, opener ports.RenderDeviceOpener) (*drmBackend, error) {
	log := logging.For(ctx, "drm")
	s, err := seat.Open(ctx, logging.For(ctx, "seat"))
	if err != nil {
		log.Error().Err(err).Msg("open seat")
		return nil, fmt.Errorf("open seat: %w", err)
	}
	want := wantFromConfig(cfg)
	want.TraceFlips = logging.Enabled(ctx, "drm-flip")
	cands, err := drm.ListGPUs("/sys", "/dev")
	if err != nil {
		s.Close()
		return nil, err
	}
	plans, sel, err := selectGPUPlans(cands, cfg, os.Getenv("NEFERWL_RENDER_DEVICE"))
	if err != nil {
		s.Close()
		return nil, err
	}
	for _, reason := range sel.Reasons {
		log.Info().Msg(reason)
	}
	b := &drmBackend{seat: s, clock: clock.System{}, log: log, opener: opener}
	s.SetBeforeDisable(b.prepareSeatDisable)
	render, info, err := openPlannedRender(opener, plans, cfg.Render.Device != "" || os.Getenv("NEFERWL_RENDER_DEVICE") != "", log)
	if err != nil {
		s.Close()
		return nil, err
	}
	b.render, b.renderInfo = render, info
	b.advertised = append([]ports.DMABufFormat(nil), render.DMABuf().Formats...)
	want.Sampled = render.DMABuf().Formats
	var errs []error
	for _, p := range plans {
		info := p.Info
		card, err := b.openCard(ctx, info, multiGPUMode(cfg.Render.MultiGPU), want, logging.Enabled(ctx, "render-damage"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		b.cards = append(b.cards, card)
	}
	if len(b.cards) == 0 {
		render.Close()
		s.Close()
		err = fmt.Errorf("no usable DRM card: %w", errors.Join(errs...))
		log.Error().Err(err).Msg("open drm")
		return nil, err
	}
	return b, nil
}

// openCard opens one non-render-device card through the seat: the shared
// body of the startup scan and of GPU hotplug. A failed card leaves no fd.
func (b *drmBackend) openCard(ctx context.Context, info ports.GPUInfo, mode drm.MultiGPU, want drm.Want, traceDamage bool) (*drmCard, error) {
	path, log := info.Path, b.log
	fd, err := b.seat.OpenDevice(path)
	if err != nil {
		return nil, err
	}
	var local *localRenderDevice
	dev := drm.CardDevices{Render: b.render, RenderCard: b.renderInfo.Path, Mode: mode}
	if path != b.renderInfo.Path {
		local = &localRenderDevice{open: func() (ports.RenderDevice, error) {
			device, err := b.opener.Open(info)
			if err != nil {
				log.Error().Err(err).Str("card", path).Msg("open local render device")
			}
			return device, err
		}}
		dev.Local = local.get
	}
	card, err := drm.OpenCard(fd, info, dev, want, log, logging.For(ctx, "render"))
	if err != nil {
		log.Warn().Err(err).Str("card", path).Msg("card unusable")
		b.seat.CloseDevice(fd)
		return nil, err
	}
	card.SetRendererOptions(ports.RendererOptions{TraceDamage: traceDamage})
	device := card.Device()
	log.Info().Str("path", path).Str("device", fmt.Sprintf("%d:%d", unix.Major(device), unix.Minor(device))).Msg("opened DRM card")
	return &drmCard{Card: card, fd: fd, local: local}, nil
}

// wantFromConfig turns output.<name> and render.* entries into connector choices.
func wantFromConfig(cfg ports.Config) drm.Want {
	r := cfg.Render
	want := drm.Want{Disabled: map[string]bool{}, Modes: map[string][3]float64{}, HDR: map[string]drm.HDRSettings{}, NoScanout: !r.DirectScanout, NoTearing: !r.Tearing, NoVRR: !r.VRR, VRRFlipGap: r.VRRFlipGap}
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
	cards := slices.Clone(b.cards)
	for c := range b.dying {
		cards = append(cards, c)
	}
	for _, c := range cards {
		c.Card.Close()
	}
	for _, c := range cards {
		if c.local != nil {
			c.local.close()
		}
	}
	if b.render != nil {
		b.render.Close()
	}
	for _, c := range cards {
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
func (b *drmBackend) runOutputs(ctx context.Context, want func(ports.Config) drm.Want, initial ports.Config, apply *outputApply, ch outputChannels, curs *cursors, log zerowrap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	var readers sync.WaitGroup
	// A reader that fails sends at most once; cards come and go, so the
	// buffer is generous and a full one drops (the first failure ends the run).
	readerErr := make(chan readerFailure, 16)
	startReader := func(c *drmCard) {
		rctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		c.stopReader, c.readerDone = stop, done
		readers.Go(func() {
			defer close(done)
			if err := safe("drm events", func() error { return c.ReadEvents(rctx) }); err != nil {
				select {
				case readerErr <- readerFailure{c, fmt.Errorf("%s: %w", c.Path(), err)}:
				default:
				}
			}
		})
	}
	for _, c := range b.cards {
		c.SetFormats(ch.formats)
		startReader(c)
	}
	defer func() { cancel(); readers.Wait() }()
	active := b.seat.Subscribe()
	defer b.seat.Unsubscribe(active)
	seatActive := true // openDRM waits for the first seat enable
	set := newOutputSet(ctx, ch.captured, ch.routes())
	set.attach = ch.content
	set.wireSecurity(ch)
	limiter := reopenLimiter{clock: b.clock}
	recovering := false
	protected := func() bool { return set.state.Protected || securitySnapshot(ch.security).Protected }
	leases := newLeasePublisher(ctx, ch.leaseEvents, func() bool { return seatActive }, protected, log)
	defer leases.close()
	hotplug := make(chan drm.HotplugEvent, 8)
	go func() {
		if err := safe("hotplug", func() error { return drm.WatchHotplug(ctx, hotplug) }); err != nil {
			log.Warn().Err(err).Msg("hotplug watch disabled")
		}
	}()
	cards := map[string]*drmCard{}
	outputs := map[string]*drm.Output{} // owner result, read only after done
	send := func(ev ports.OutputEvent) {
		// Only the select is timed: the router logs the longest wait, which
		// tells whether core holds up the backend loop.
		start := time.Now()
		select {
		case ch.events <- ev:
		case <-ctx.Done():
		}
		set.router.noteEventSend(time.Since(start))
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
	// A small bounded retry budget avoids a busy loop on failed KMS revokes.
	revokeTimer := b.clock.NewTimer(time.Second)
	revokeTimer.Stop()
	defer revokeTimer.Stop()
	var revokeDeadline <-chan time.Time
	var revokeAttempts int
	var discardedOutputErr error // first unconfirmed lifetime; inventory retains every instance
	barrier := func() {
		if !set.state.Protected {
			return
		}
		var errs []error
		for _, c := range b.cards {
			errs = append(errs, revokeSecurityLeases(c, func(id uint32) {
				leases.send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
			}))
			leases.publish(c)
		}
		errs = append(errs, discardedOutputErr)
		err := errors.Join(errs...)
		// Registration runs synchronously in scan before any barrier. A gate
		// transition racing native revocation belongs to a later round.
		if securitySnapshot(ch.security).Generation > set.state.Generation {
			return
		}
		set.securityEvent(ports.SecurityBackendBarrier{Generation: set.state.Generation, Err: err})
		revokeAttempts++
		if err != nil && revokeAttempts < 3 {
			revokeTimer.Reset(time.Second)
			revokeDeadline = revokeTimer.C()
		}
	}
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
		if recovering {
			return
		}
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
			leases.publish(c)
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
				// Every physical lifetime gets the gate before any early exit:
				// Close must not restore saved desktop, even on a name collision.
				o.Security, o.Frames = ch.security, ch.frames
				name := o.Info().Name
				if other := cards[name]; other != nil && other != c {
					// Core and clients know outputs by connector name.
					log.Warn().Str("connector", name).Str("card", c.Path()).Msg("connector name already used by another card; ignored")
					instance, registerErr := registerDiscardedOutput(set, name)
					o.Instance = instance
					o.Close() // synchronous final KMS disable; never starts a renderer
					var shutdownErr error
					if registerErr == nil {
						shutdownErr = retireDiscardedOutput(set, instance, o.InactiveOnClose())
					}
					if err := errors.Join(registerErr, shutdownErr); err != nil {
						if discardedOutputErr == nil {
							discardedOutputErr = err
						}
						scanErrors = append(scanErrors, err)
						log.Error().Err(err).Str("connector", name).Str("card", c.Path()).Msg("discarded output protection unconfirmed")
						// An exhausted instance cannot represent an unsafe lifetime.
						// Stop the backend rather than ever acknowledge this inventory.
						if instance == 0 {
							cancel()
						}
					}
					c.Release(name)
					continue
				}
				cards[name] = c
				outputs[name] = o
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
				realtime := currentConfig.Performance.Realtime
				started := set.start(ctx, name, func(octx context.Context, feed outputFeed) error {
					defer b.seat.Unsubscribe(active)
					o.Security, o.SecurityChanges, o.Instance, o.StartOff = ch.security, feed.Security, feed.Instance, feed.StartOff
					return safe("output "+name, func() error {
						defer o.Close()
						b.addPreparer(o)
						defer b.removePreparer(o) // before Close: it handles no request any more
						// octx derives from the session: the probe ends with this
						// output or the session.
						b.addProbe(name, func() { o.Ping(octx) })
						defer b.removeProbe(name)
						if realtime {
							runtime.LockOSThread()
							// Keep the thread locked until this goroutine exits: an RT
							// thread must not return to Go's general-purpose pool.
							schedLog := logging.For(ctx, "sched")
							if err := sched.Realtime(schedLog); err != nil {
								schedLog.Warn().Err(err).Msg("output scheduling")
							}
						}
						return o.Run(octx, loadCursor, active, feed.Scenes, feed.Contents, feed.Returns, feed.Cursor, ch.presented, feed.Captures, ch.captured)
					})
				}, &o.SecurityEvents)
				if !started {
					b.seat.Unsubscribe(active)
					o.Close()
					c.Release(name)
					continue
				}
				send(ports.OutputAdded{Info: o.Info()})
			}
		}
		publishInventory()
		complete(progress.scanError(errors.Join(scanErrors...)))
		barrier()
	}
	// Cards plugged in while the render device is being reopened wait here.
	var pending, replug pendingCards
	// addCard opens a hot-plugged card and starts its reader; the caller
	// scans. The dmabuf feedback tables are computed at startup, so outputs
	// of a hot-added card do not change what clients are told.
	addCard := func(name string) bool {
		c, action := b.hotplugAdd(ctx, name, currentConfig, want(currentConfig))
		if action == hotplugAddDying {
			replug.add(name) // finishCard reopens it
		}
		if c == nil {
			return false
		}
		c.SetFormats(ch.formats)
		b.cards = append(b.cards, c)
		startReader(c)
		return true
	}
	// finishCard releases an unplugged card once its outputs are closed. The
	// reader is joined first: its fd is about to close.
	finishCard := func(c *drmCard) {
		c.stopReader()
		<-c.readerDone
		c.Card.Close()
		if c.local != nil {
			c.local.close()
		}
		b.seat.CloseDevice(c.fd)
		delete(b.dying, c)
		delete(lastHeads, c)
		log.Info().Str("card", c.Path()).Msg("GPU removed")
		if name := filepath.Base(c.Path()); replug.has(name) {
			replug.remove(name)
			if recovering {
				pending.add(name)
			} else if addCard(name) {
				// The scan comes from the loop, not from inside a stop handler.
				select {
				case hotplug <- drm.HotplugEvent{}:
				default:
				}
			}
		}
	}
	// removeCard takes an unplugged card out of service: no scan sees it, its
	// leases are revoked and its outputs stop through the normal path.
	removeCard := func(name string) {
		idx := slices.IndexFunc(b.cards, func(c *drmCard) bool { return filepath.Base(c.Path()) == name })
		if idx < 0 {
			return
		}
		c := b.cards[idx]
		c.removed = true
		b.cards = slices.Delete(b.cards, idx, idx+1)
		log.Info().Str("card", c.Path()).Msg("GPU unplugged")
		if err := revokeSecurityLeases(c, func(id uint32) { leases.send(ports.LeaseFinished{Card: c.Path(), LeaseID: id}) }); err != nil {
			log.Warn().Err(err).Str("card", c.Path()).Msg("revoke leases of removed GPU")
		}
		leases.forget(c)
		var names []string
		for n, owner := range cards {
			if owner == c && set.outs[n] != nil {
				names = append(names, n)
			}
		}
		td := newCardTeardown(names)
		if b.dying == nil {
			b.dying = map[*drmCard]*cardTeardown{}
		}
		b.dying[c] = td
		for _, n := range names {
			stopping[n] = false
			progress.stopping(n)
			set.outs[n].stop()
		}
		publishInventory()
		if td.done() {
			finishCard(c)
		}
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
		case failure := <-readerErr:
			if failure.card.removed {
				continue // the card is gone; its outputs are being stopped
			}
			if path := failure.card.Path(); path != b.renderInfo.Path && !deviceNodeExists(path) {
				// The read fails as the card unplugs, possibly before its
				// uevent is handled.
				log.Warn().Err(failure.err).Str("card", path).Msg("card reader failed, device node gone")
				removeCard(filepath.Base(path))
				continue
			}
			// No more flips on that card: its outputs would freeze.
			return errors.Join(failure.err, set.wait())
		case state, ok := <-ch.securityChanges:
			if !ok {
				ch.securityChanges = nil
				continue
			}
			if state.Generation < set.state.Generation {
				continue
			}
			if set.setSecurity(state) {
				revokeAttempts = 0
			}
			// start can refresh the gate while scanning; its queued transition
			// must still receive a barrier even if forwarding already occurred.
			revokeTimer.Stop()
			revokeDeadline = nil
			if set.state.Protected {
				barrier()
			} else {
				for _, c := range b.cards {
					leases.publish(c)
				}
			}
		case <-revokeDeadline:
			revokeDeadline = nil
			barrier()
		case ev := <-hotplug:
			switch classifyHotplug(ev, b.renderInfo.Path) {
			case hotplugRenderRemoved:
				// Checked even while recovering: the device is not coming back.
				path := b.renderInfo.Path
				log.Error().Str("card", path).Msg("render GPU removed")
				return errors.Join(fmt.Errorf("%w: %s", ErrRenderGPURemoved, path), set.wait())
			case hotplugCardAdded:
				if recovering {
					// Opening needs the reopened render device.
					pending.add(ev.Card)
					continue
				}
				if !addCard(ev.Card) {
					continue
				}
			case hotplugCardRemoved:
				pending.remove(ev.Card)
				replug.remove(ev.Card)
				removeCard(ev.Card)
				continue
			}
			log.Info().Str("card", ev.Card).Msg("hotplug")
			scan()
			if progress.active {
				running := map[string]<-chan error{}
				for name := range set.outs {
					running[name] = readySources[name]
				}
				complete(progress.scanned(running))
			}
		case enabled := <-active:
			seatActive = enabled
			if !enabled {
				for _, c := range b.cards {
					for _, id := range c.LeaseIDs() {
						if err := c.Revoke(id); err != nil {
							log.Warn().Err(err).Uint32("lease", id).Msg("VT lease revoke")
						} else {
							leases.send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
						}
					}
					leases.publish(c)
				}
			} else {
				for _, c := range b.cards {
					leases.publish(c)
				}
			}
		case event := <-ch.leaseRequests:
			switch req := event.(type) {
			case ports.LeaseRequest:
				reply := ports.LeaseReply{ID: req.ID, Err: fmt.Errorf("unknown card %s", req.Card)}
				if !seatActive || protected() {
					reply.Err = errors.New("DRM seat inactive or session protected")
					leases.send(reply)
					break
				}
				for _, c := range b.cards {
					if c.Path() == req.Card {
						reply.FD, reply.LeaseID, reply.Err = guardedLease(c, ch.security, req.Connectors)
						leases.publish(c)
						break
					}
				}
				leases.send(reply)
			case ports.LeaseRevoke:
				for _, c := range b.cards {
					if c.Path() == req.Card {
						if err := c.Revoke(req.LeaseID); err != nil {
							log.Warn().Err(err).Uint32("lease", req.LeaseID).Msg("revoke lease")
						} else {
							leases.publish(c)
						}
						break
					}
				}
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
		case result := <-ready:
			complete(progress.readyEvent(result.name, result.source, readySources[result.name], result.err))
		case <-deadline:
			complete(progress.timeout(op))
		case stopped := <-set.stopped:
			if !set.stoppedCurrent(stopped) {
				continue
			}
			name := stopped.name
			output := outputs[name]
			err := set.finish(name)
			// finish observes done after the deferred Close. Only affirmative
			// terminal KMS inactivity retires this exact lifetime; renderer
			// errors and missing connector inventory are not physical proof.
			if output != nil && output.InactiveOnClose() {
				set.securityEvent(ports.SecurityOutputRemoved{Instance: stopped.instance})
			}
			delete(outputs, name)
			if ctx.Err() != nil {
				return errors.Join(err, set.wait())
			}
			if !recovering && errors.Is(err, ports.ErrDeviceLost) {
				recovering = true
				// The backend currently owns one shared render device. Cancel every
				// consumer first; normal stopped handling joins and retires each lifetime.
				for n, r := range set.outs {
					stopping[n] = false
					r.stop()
				}
			}
			restart := stopping[name]
			delete(stopping, name)
			curs.set(name, nil)
			card := cards[name]
			card.Release(name)
			lastHeads[card] = releasedHead(lastHeads[card], name)
			delete(cards, name)
			if td := b.dying[card]; td != nil && td.outputStopped(name) {
				finishCard(card)
			}
			source := readySources[name]
			delete(readySources, name)
			if stopped := stopReady[name]; stopped != nil {
				close(stopped)
				delete(stopReady, name)
			}
			stopDecision := progress.stopped(name, source, restart, err)
			if recovering {
				complete(stopDecision)
				send(ports.OutputRemoved{Name: name})
				if len(set.outs) > 0 {
					continue
				}
				old := b.render
				b.render = nil // failure cleanup must not close the lost device again.
				log.Warn().Str("card", b.renderInfo.Path).Msg("render device lost, reopening")
				next, reopenErr := reopenRender(b.opener, b.renderInfo, old, &limiter)
				if reopenErr != nil {
					return errors.Join(reopenErr, set.wait())
				}
				b.render = next
				if lost := lostFormats(b.advertised, next.DMABuf().Formats); lost > 0 {
					log.Warn().Str("card", b.renderInfo.Path).Int("lost_formats", lost).Msg("render device lost advertised formats")
				}
				for _, c := range b.cards {
					c.SetRender(next)
				}
				recovering = false
				for _, name := range pending.take() {
					addCard(name)
				}
				scan()
				continue
			}

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
			send(ports.OutputRemoved{Name: name})
			running := map[string]<-chan error{}
			for n := range set.outs {
				running[n] = readySources[n]
			}
			complete(progress.scanned(running))
		}
	}
}

func deviceNodeExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// localRenderDevice opens the lazy local device on first use and caches only
// success: a failed open is retried by the next caller. Outputs are joined
// before close; the mutex protects concurrent first-use opens.
type localRenderDevice struct {
	mu   sync.Mutex
	open func() (ports.RenderDevice, error)
	dev  ports.RenderDevice
}

func (l *localRenderDevice) get() (ports.RenderDevice, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dev != nil {
		return l.dev, nil
	}
	dev, err := l.open()
	if err != nil {
		return nil, err
	}
	l.dev = dev
	return dev, nil
}

func (l *localRenderDevice) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dev != nil {
		l.dev.Close()
		l.dev = nil
	}
}
