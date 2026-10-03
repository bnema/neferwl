package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/seat"
	"github.com/bnema/neferwl/internal/logging"
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
	want.TraceFlips = logging.Enabled(ctx, "drm-flip")
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
	active := b.seat.Subscribe()
	defer b.seat.Unsubscribe(active)
	seatActive := true // openDRM waits for the first seat enable
	set := newOutputSet(ctx, ch.captured)
	set.wireSecurity(ch)
	protected := func() bool { return set.state.Protected || securitySnapshot(ch.security).Protected }
	clientFDs := map[*drmCard]*os.File{}
	lastLeaseConnectors := map[*drmCard][]ports.LeaseConnector{}
	defer func() {
		for _, f := range clientFDs {
			f.Close()
		}
	}()
	sendLease := func(msg ports.LeaseMessage) {
		select {
		case ch.leaseEvents <- msg:
		case <-ctx.Done():
			ports.CloseLeaseFiles(msg)
		}
	}
	publishLeases := func(c *drmCard) {
		for _, id := range c.FinishedLeases() {
			sendLease(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
		}
		var next []ports.LeaseConnector
		if seatActive && !protected() {
			next = c.Leasable()
		}
		if len(next) == 0 {
			if protected() || clientFDs[c] != nil {
				sendLease(ports.LeaseConnectors{Card: c.Path()})
			}
			if clientFDs[c] != nil {
				clientFDs[c].Close()
				delete(clientFDs, c)
			}
			delete(lastLeaseConnectors, c)
			return
		}
		if slices.Equal(lastLeaseConnectors[c], next) && clientFDs[c] != nil {
			return
		}
		if clientFDs[c] == nil {
			f, err := c.ClientFD()
			if err != nil {
				log.Warn().Err(err).Str("card", c.Path()).Msg("lease client fd")
				return
			}
			clientFDs[c] = f
		}
		// Each message owns its fd: a queued inventory outlives clientFDs[c].
		fd, err := unix.FcntlInt(clientFDs[c].Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			log.Warn().Err(err).Str("card", c.Path()).Msg("lease client fd")
			return
		}
		lastLeaseConnectors[c] = next
		sendLease(ports.LeaseConnectors{Card: c.Path(), Device: os.NewFile(uintptr(fd), c.Path()), Connectors: next})
	}
	hotplug := make(chan struct{}, 1)
	go func() {
		if err := safe("hotplug", func() error { return drm.WatchHotplug(ctx, hotplug) }); err != nil {
			log.Warn().Err(err).Msg("hotplug watch disabled")
		}
	}()
	cards := map[string]*drmCard{}
	outputs := map[string]*drm.Output{} // owner result, read only after done
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
				sendLease(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
			}))
			publishLeases(c)
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
			publishLeases(c)
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
				o.Security = ch.security
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
				o.NewCaptureRenderer = newRenderer
				o.StartOff = set.off[name]
				progress.started(name, source)
				if cur := o.Cursor(); cur != nil {
					curs.set(name, cur)
				}
				active := b.seat.Subscribe()
				realtime := currentConfig.Performance.Realtime
				started := set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest, secure <-chan ports.SecurityState, instance ports.OutputInstance) error {
					defer b.seat.Unsubscribe(active)
					o.Security, o.SecurityChanges, o.Instance = ch.security, secure, instance
					return safe("output "+name, func() error {
						defer o.Close()
						if realtime {
							runtime.LockOSThread()
							// Keep the thread locked until this goroutine exits: an RT
							// thread must not return to Go's general-purpose pool.
							if err := sched.Realtime(log); err != nil {
								log.Warn().Str("component", "sched").Err(err).Msg("output scheduling")
							}
						}
						return o.Run(octx, newRenderer, loadCursor, active, sc, cc, cu, ch.presented, cap, ch.captured)
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
		contents, contentOut, contentNext, contentOwner := set.contentOut(ch.contents)
		select {
		case <-ctx.Done():
			return set.wait()
		case err := <-readerErr:
			// No more flips on that card: its outputs would freeze.
			return errors.Join(err, set.wait())
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
					publishLeases(c)
				}
			}
		case <-revokeDeadline:
			revokeDeadline = nil
			barrier()
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
		case enabled := <-active:
			seatActive = enabled
			if !enabled {
				for _, c := range b.cards {
					for _, id := range c.LeaseIDs() {
						if err := c.Revoke(id); err != nil {
							log.Warn().Err(err).Uint32("lease", id).Msg("VT lease revoke")
						} else {
							sendLease(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
						}
					}
					publishLeases(c)
				}
			} else {
				for _, c := range b.cards {
					publishLeases(c)
				}
			}
		case event := <-ch.leaseRequests:
			switch req := event.(type) {
			case ports.LeaseRequest:
				reply := ports.LeaseReply{ID: req.ID, Err: fmt.Errorf("unknown card %s", req.Card)}
				if !seatActive || protected() {
					reply.Err = errors.New("DRM seat inactive or session protected")
					sendLease(reply)
					break
				}
				for _, c := range b.cards {
					if c.Path() == req.Card {
						reply.FD, reply.LeaseID, reply.Err = guardedLease(c, ch.security, req.Connectors)
						publishLeases(c)
						break
					}
				}
				sendLease(reply)
			case ports.LeaseRevoke:
				for _, c := range b.cards {
					if c.Path() == req.Card {
						if err := c.Revoke(req.LeaseID); err != nil {
							log.Warn().Err(err).Uint32("lease", req.LeaseID).Msg("revoke lease")
						} else {
							publishLeases(c)
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
		case s := <-ch.scenes:
			set.scenes(s)
		case c := <-contents:
			set.content(c)
		case contentOut <- contentNext:
			contentOwner.contentSent()
		case q := <-ch.captures:
			set.routeCapture(q)
		case c := <-ch.cursorChanges:
			set.setCursor(c)
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
			send(ports.OutputRemoved{Name: name})
			running := map[string]<-chan error{}
			for n := range set.outs {
				running[n] = readySources[n]
			}
			complete(progress.scanned(running))
		}
	}
}
