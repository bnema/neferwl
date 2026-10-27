package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/seat"
	"github.com/bnema/neferwl/internal/adapters/watchdog"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// securityLeaseCard keeps the native operation and its tracked lease inventory
// together. A failed revoke must remain in LeaseIDs for the next barrier attempt.
type securityLeaseCard interface {
	Path() string
	Lease([]string) (*os.File, uint32, error)
	LeaseIDs() []uint32
	Revoke(uint32) error
}

func securitySnapshot(gate ports.SessionSecurity) domain.SecurityState {
	if gate == nil {
		return domain.SecurityState{}
	}
	return gate.Snapshot()
}

// guardedLease denies requests pending at acquisition and closes any FD created
// across a transition, even when the native call succeeds. Failed revocations
// stay tracked by the card and prevent the later backend barrier.
func guardedLease(c securityLeaseCard, gate ports.SessionSecurity, connectors []string) (*os.File, uint32, error) {
	before := securitySnapshot(gate)
	if err := core.LeaseGuard(before, before); err != nil {
		return nil, 0, err
	}
	fd, id, err := c.Lease(connectors)
	if err != nil {
		return fd, id, err
	}
	after := securitySnapshot(gate)
	if err := core.LeaseGuard(before, after); err != nil {
		if fd != nil {
			fd.Close()
		}
		return nil, 0, errors.Join(err, c.Revoke(id))
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
func registerDiscardedOutput(set *routing.Set, name string) (domain.OutputInstance, error) {
	instance, err := core.RegisterDiscardedOutput(set.NextInstance)
	if err != nil {
		set.StartErr = err
		return 0, err
	}
	set.NextInstance = instance
	if !set.SecurityEvent(ports.SecurityOutputAdded{Instance: instance, Output: name}) {
		return instance, set.Ctx.Err()
	}
	return instance, nil
}

// retireDiscardedOutput consumes only affirmative terminal evidence. A failed
// close remains in authoritative inventory, even though it has no render owner.
func retireDiscardedOutput(set *routing.Set, instance domain.OutputInstance, inactive bool) error {
	if err := core.RetireDiscardedOutput(instance, inactive); err != nil {
		return err
	}
	if !set.SecurityEvent(ports.SecurityOutputRemoved{Instance: instance}) {
		return set.Ctx.Err()
	}
	return nil
}

// applyTimeout bounds how long outputs take to apply a configuration.
const applyTimeout = 5 * time.Second

// Backend is the seat and the cards with at least one usable output.
type Backend struct {
	startupConfig domain.Config
	selectConfig  core.GPUSelectConfig
	render        ports.RenderDevice
	opener        ports.RenderDeviceOpener
	renderInfo    domain.GPUInfo
	advertised    []domain.DMABufFormat
	seat          *seat.Seat
	cards         []*drmCard
	// dying holds unplugged cards until their outputs are stopped. Only the
	// runOutputs goroutine touches it; close reads it after that returns.
	dying map[*drmCard]*core.CardTeardown
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

func (b *Backend) addProbe(name string, ping func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probes == nil {
		b.probes = map[string]func(){}
	}
	b.probes[name] = ping
}

func (b *Backend) removeProbe(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.probes, name)
}

// watchdogProbes lists the running output loops for the watchdog.
func (b *Backend) WatchdogProbes() []watchdog.Probe {
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

func (b *Backend) addPreparer(p seatDisablePreparer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.preparers == nil {
		b.preparers = map[seatDisablePreparer]struct{}{}
	}
	b.preparers[p] = struct{}{}
}

func (b *Backend) removePreparer(p seatDisablePreparer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.preparers, p)
}

// prepareSeatDisable runs on the seat's goroutine before the seat is
// disabled and its devices revoked: every running output commits what it
// must leave behind (planes on Bypass), all in parallel under one bound. A
// timeout is logged and the disable goes on.
func (b *Backend) prepareSeatDisable() {
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
	removed   bool
	localLost bool
	// stopReader and readerDone belong to the card's ReadEvents goroutine.
	stopReader context.CancelFunc
	readerDone <-chan struct{}
}

// A failed card read must not erase its heads from a multi-card inventory.
func retainedHeads(previous, found []domain.OutputHead, err error) []domain.OutputHead {
	if err != nil {
		return previous
	}
	return found
}

// A stopped instance is no longer enabled, even if the inventory read fails.
func releasedHead(heads []domain.OutputHead, name string) []domain.OutputHead {
	heads = append([]domain.OutputHead(nil), heads...)
	for i := range heads {
		if heads[i].Info.Name == name {
			heads[i].Enabled = false
			heads[i].Current = nil
		}
	}
	return heads
}

// openDRM opens the seat and every card with a connected display. Outputs
// are found by the first Scan of each card.
func Open(ctx context.Context, cfg domain.Config, opener ports.RenderDeviceOpener) (*Backend, error) {
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
	selectConfig, err := drm.SelectConfig(cfg, os.Getenv("NEFERWL_RENDER_DEVICE"))
	if err != nil {
		s.Close()
		return nil, err
	}
	sel, plans, err := selectAndPlanGPUs(gpuPolicy{}, cands, selectConfig)
	if err != nil {
		s.Close()
		return nil, err
	}
	for _, reason := range sel.Reasons {
		log.Info().Msg(reason)
	}
	b := &Backend{startupConfig: cfg, selectConfig: selectConfig, seat: s, clock: clock.System{}, log: log, opener: opener}
	s.SetBeforeDisable(b.prepareSeatDisable)
	render, info, err := openPlannedRender(opener, plans, cfg.Render.Device != "" || os.Getenv("NEFERWL_RENDER_DEVICE") != "", log)
	if err != nil {
		s.Close()
		return nil, err
	}
	b.render, b.renderInfo = render, info
	b.advertised = append([]domain.DMABufFormat(nil), render.DMABuf().Formats...)
	want.Sampled = render.DMABuf().Formats
	var errs []error
	for _, p := range plans {
		info := p.Info
		card, err := b.openCard(ctx, info, domain.ParseMultiGPUMode(cfg.Render.MultiGPU), want, logging.Enabled(ctx, "render-damage"))
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

// openCard opens one kept card; the render card gets no local device.
// Startup and GPU hotplug share this path. A failed card leaves no fd.
func (b *Backend) openCard(ctx context.Context, info domain.GPUInfo, mode domain.MultiGPUMode, want drm.Want, traceDamage bool) (*drmCard, error) {
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
	card.SetRendererOptions(domain.RendererOptions{TraceDamage: traceDamage})
	device := card.Device()
	log.Info().Str("path", path).Str("driver", info.Driver).Str("device", fmt.Sprintf("%d:%d", unix.Major(device), unix.Minor(device))).Msg("opened DRM card")
	sf, only := card.ScanoutFormats(), 0
	for _, f := range sf {
		if !slices.Contains(want.Sampled, f) {
			only++
		}
	}
	log.Debug().Str("path", path).Int("scanout_formats", len(sf)).Int("scanout_only", only).Msg("card scanout formats")
	return &drmCard{Card: card, fd: fd, local: local}, nil
}

// wantFromConfig turns output.<name> and render.* entries into connector choices.
func wantFromConfig(cfg domain.Config) drm.Want {
	r := cfg.Render
	want := drm.Want{Disabled: map[string]bool{}, Modes: map[string][3]float64{}, HDR: map[string]drm.HDRSettings{}, NoScanout: !r.DirectScanout, NoTearing: !r.Tearing, NoVRR: !r.VRR, VRRFlipGap: r.VRRFlipGap}
	for _, o := range cfg.Outputs {
		nits := o.SDRBrightness
		if nits == 0 {
			nits = domain.DefaultSDRBrightness
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
func (b *Backend) Close() {
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

// readerFailure is the error of one card's ReadEvents goroutine.
type readerFailure struct {
	card *drmCard
	err  error
}

// hotplugAdd opens a card that just appeared, or returns nil with the action
// that explains why not. It runs on the runOutputs goroutine.
func (b *Backend) hotplugAdd(ctx context.Context, name domain.CardName, want drm.Want) (*drmCard, core.HotplugAddAction) {
	log := b.log
	cands, err := drm.ListGPUs("/sys", "/dev")
	if err != nil {
		log.Warn().Err(err).Str("card", string(name)).Msg("GPU hotplug: list GPUs")
		return nil, core.HotplugAddUnknown
	}
	var existing, dying []domain.CardName
	for _, c := range b.cards {
		existing = append(existing, domain.CardName(filepath.Base(c.Path())))
	}
	for c := range b.dying {
		dying = append(dying, domain.CardName(filepath.Base(c.Path())))
	}
	info, action, sel, err := core.PlanHotplugAdd(cands, name, b.selectConfig, existing, dying)
	if err != nil {
		log.Warn().Err(err).Str("card", string(name)).Msg("GPU hotplug: selection failed, card ignored")
	}
	switch action {
	case core.HotplugAddUnknown:
		log.Warn().Str("card", string(name)).Msg("GPU hotplug: card not listed, ignored")
	case core.HotplugAddExisting:
		log.Info().Str("card", string(name)).Msg("GPU hotplug: card already open")
	case core.HotplugAddDying:
		log.Info().Str("card", string(name)).Msg("GPU hotplug: card still shutting down, will retry")
	case core.HotplugAddIgnored:
		log.Info().Str("card", string(name)).Msg("GPU hotplug: card not selected by drm.devices/drm.ignore, not opened")
	case core.HotplugAddOpen:
		for _, reason := range sel.Reasons {
			log.Info().Msg(reason)
		}
		c, err := b.openCard(ctx, info, domain.ParseMultiGPUMode(b.startupConfig.Render.MultiGPU), want, logging.Enabled(ctx, "render-damage"))
		if err != nil {
			log.Warn().Err(err).Str("card", string(name)).Msg("GPU hotplug: card unusable, not retried")
			return nil, core.HotplugAddFailed
		}
		return c, action
	}
	return nil, action
}

// reopenRender closes the old device once and charges every open attempt.
//
//hexcheck:ignore suspicious-business-logic-in-adapter closes and opens native render devices in order; core ReopenLimiter owns the retry budget
func reopenRender(opener ports.RenderDeviceOpener, info domain.GPUInfo, old ports.RenderDevice, limiter *core.ReopenLimiter) (ports.RenderDevice, error) {
	if old != nil {
		old.Close()
	}
	var lastErr error
	for limiter.Allow() {
		dev, err := opener.Open(info)
		if err == nil {
			return dev, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("render device %s: %w: %w", info.Path, ports.ErrDeviceLost, lastErr)
	}
	return nil, fmt.Errorf("render device %s: %w", info.Path, ports.ErrDeviceLost)
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

func (b *Backend) RenderDevice() ports.RenderDevice { return b.render }
func (b *Backend) Seat() *seat.Seat                 { return b.seat }

func logOutputApplied(heads []domain.HeadChange, log zerowrap.Logger) {
	for _, h := range heads {
		e := log.Info().Str("output", h.Name).Bool("enabled", h.Enabled).Float64("scale", h.Scale)
		if h.Transform != nil {
			e = e.Uint8("transform", uint8(*h.Transform))
		}
		e.Msg("output configuration applied")
	}
}

func openPlannedRender(opener ports.RenderDeviceOpener, plans []core.CardPlan, strict bool, log zerowrap.Logger) (ports.RenderDevice, domain.GPUInfo, error) {
	ordered := core.RenderOpenOrder(plans, strict)
	var errs []error
	for _, info := range ordered {
		dev, err := opener.Open(info)
		if err == nil {
			return dev, info, nil
		}
		log.Warn().Err(err).Str("card", info.Path).Msg("render GPU unavailable")
		errs = append(errs, fmt.Errorf("render card %s: %w", info.Path, err))
	}
	if len(errs) == 0 {
		errs = append(errs, core.ErrNoRenderGPU)
	}
	return nil, domain.GPUInfo{}, errors.Join(errs...)
}
