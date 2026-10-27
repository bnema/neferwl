package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// RunOutputs drives every output of every card: one goroutine per output,
// one flip reader per card, and a udev watcher that rescans connectors on
// hotplug. Core learns about outputs through events. The loop also owns
// output configuration: reloads and protocol requests go through apply, and
// each configuration is finished when its outputs are ready, fail or time
// out. It returns when ctx ends, after every output is closed.
func (b *Backend) RunOutputs(ctx context.Context, initial domain.Config, apply *core.OutputApply, ch routing.Channels, curs *cursors.Router, log zerowrap.Logger) error {
	run := outputsRun{b: b, ctx: ctx, apply: apply, ch: ch, curs: curs, log: log}
	run.ctx, run.cancel = context.WithCancel(run.ctx)
	// A reader that fails sends at most once; cards come and go, so the
	// buffer is generous and a full one drops (the first failure ends the run).
	run.readerErr = make(chan readerFailure, 16)
	for _, c := range run.b.cards {
		c.SetFormats(run.ch.Formats)
		run.startReader(c)
	}
	defer func() { run.cancel(); run.readers.Wait() }()
	run.active = run.b.seat.Subscribe()
	defer b.seat.Unsubscribe(run.active)
	run.seatActive = true // openDRM waits for the first seat enable
	run.set = routing.NewSet(run.ctx, run.ch.Captured, run.ch.Routes())
	run.set.Attach = run.ch.Content
	run.set.WireSecurity(run.ch)
	run.policy = core.OutputPolicy{RenderCard: run.b.renderInfo.Name, ReopenLimiter: core.ReopenLimiter{Clock: run.b.clock}}
	protected := func() bool { return run.set.State.Protected || securitySnapshot(run.ch.Security).Protected }
	run.leases = routing.NewLeasePublisher(run.ctx, run.ch.LeaseEvents, func() bool { return run.seatActive }, protected, run.log)
	defer run.leases.Close()
	run.hotplug = make(chan domain.HotplugEvent, 8)
	go func() {
		if err := safe("hotplug", func() error { return drm.WatchHotplug(run.ctx, run.hotplug) }); err != nil {
			run.log.Warn().Err(err).Msg("hotplug watch disabled")
		}
	}()
	run.cards = map[string]*drmCard{}
	run.outputs = map[string]*drm.Output{} // owner result, read only after done
	// stopping holds outputs asked to stop: true when they restart with a
	// new mode (core keeps the screen), false when they are gone.
	run.stopping = map[string]bool{}
	run.currentConfig = initial
	run.lastHeads = map[*drmCard][]domain.OutputHead{}
	run.progress = &core.ApplyProgress{}
	run.readySources = map[string]domain.OutputInstance{}
	run.stopReady = map[string]chan struct{}{}
	run.ready = make(chan readyResult)
	// deadline is the pending apply's timer channel, nil while none is pending.
	run.timer = run.b.clock.NewTimer(applyTimeout)
	run.timer.Stop()
	defer run.timer.Stop()
	// A small bounded retry budget avoids a busy loop on failed KMS revokes.
	run.revokeTimer = run.b.clock.NewTimer(time.Second)
	run.revokeTimer.Stop()
	defer run.revokeTimer.Stop()
	run.scan()
	if len(run.set.Outs) == 0 {
		return errors.Join(errors.New("no connected display"), run.set.Wait())
	}
	return run.loop()
}

// outputsRun owns the synchronous supervisor state. Output and reader goroutines
// retain the same channels and lifetimes; extraction adds no event hops.
type outputsRun struct {
	b                  *Backend
	ctx                context.Context
	cancel             context.CancelFunc
	ch                 routing.Channels
	curs               *cursors.Router
	log                zerowrap.Logger
	apply              *core.OutputApply
	readers            sync.WaitGroup
	readerErr          chan readerFailure
	active             <-chan bool
	seatActive         bool
	set                *routing.Set
	policy             core.OutputPolicy
	leases             *routing.LeasePublisher
	hotplug            chan domain.HotplugEvent
	cards              map[string]*drmCard
	outputs            map[string]*drm.Output
	stopping           map[string]bool
	currentConfig      domain.Config
	currentHeads       domain.OutputHeads
	lastHeads          map[*drmCard][]domain.OutputHead
	progress           *core.ApplyProgress
	readySources       map[string]domain.OutputInstance
	stopReady          map[string]chan struct{}
	ready              chan readyResult
	timer              ports.Timer
	deadline           <-chan time.Time
	revokeTimer        ports.Timer
	revokeDeadline     <-chan time.Time
	discardedOutputErr error
	op                 uint64
}

type readyResult struct {
	name   string
	source domain.OutputInstance
	err    error
}
