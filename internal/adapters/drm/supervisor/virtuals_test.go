package supervisor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/outputkit/virtuals"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestSupervisorVirtualInventoryAndSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan ports.OutputEvent, 16)
		ch := routing.Channels{Events: events}
		set := routing.NewSet(ctx, nil, ch.Routes())
		defer func() { _ = set.Wait() }()
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Once()
		log := logging.For(ctx, "drm")
		apply := core.NewOutputApply(core.NewOutputOverrides(domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}}, false))
		host := virtuals.New(set, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, log)
		run := outputsRun{ctx: ctx, b: &Backend{}, ch: ch, set: set, virtuals: host, apply: apply, log: log, lastHeads: map[*drmCard][]domain.OutputHead{}, stopping: map[string]bool{}}
		if err := host.Sync(ctx, apply.Virtuals()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run.publishInventory()
		_, inventory := apply.HeadsOut(make(chan domain.OutputHeads))
		if len(inventory.Heads) != 1 || !inventory.Heads[0].Info.Virtual {
			t.Fatal(inventory)
		}
		instance := set.Outs["tv"].Instance
		if err, _ := run.onSeat(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if set.Outs["tv"].Instance != instance || set.Outs["tv"].Ctx.Err() != nil {
			t.Fatal("seat inactive stopped virtual")
		}
		apply.Reload(domain.Config{})
		if err := host.Sync(ctx, apply.Virtuals()); err != nil {
			t.Fatal(err)
		}
		run.publishInventory()
		_, inventory = apply.HeadsOut(make(chan domain.OutputHeads))
		if len(inventory.Heads) != 0 || len(set.Outs) != 0 {
			t.Fatal("removed virtual retained")
		}
	})
}

func TestRecoveryJoinsVirtualConsumers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		set := routing.NewSet(ctx, nil, routing.Routes{})
		defer func() { _ = set.Wait() }()
		if !set.Start(ctx, "tv", func(ctx context.Context, _ routing.Feed) error { <-ctx.Done(); return nil }) {
			t.Fatal("start")
		}
		run := outputsRun{set: set, stopping: map[string]bool{}}
		run.beginRecovery(ports.ErrDeviceLost)
		synctest.Wait()
		if !run.policy.Recovering || set.Outs["tv"].Ctx.Err() == nil {
			t.Fatal("virtual consumer not stopped")
		}
		// The device cannot reopen until its stopped lifetime has been joined.
		if err, exit := run.finishRecovery(); err != nil || exit {
			t.Fatalf("premature recovery: %v %v", err, exit)
		}
		if err := set.Finish("tv"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRecoveryVirtualStopDoesNotDelayFollowingApply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch := routing.Channels{Events: make(chan ports.OutputEvent, 32)}
		set := routing.NewSet(ctx, nil, ch.Routes())
		defer func() { _ = set.Wait() }()
		renderer := portsmocks.NewMockRenderer(t)
		renderer.EXPECT().Close().Return().Once()
		log := logging.For(ctx, "drm")
		host := virtuals.New(set, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return renderer, nil }, log)
		if err := host.Sync(ctx, []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		instance := set.Outs["tv"].Instance
		old := portsmocks.NewMockRenderDevice(t)
		old.EXPECT().Close().Return().Once()
		next := portsmocks.NewMockRenderDevice(t)
		next.EXPECT().DMABuf().Return(domain.DMABufSupport{})
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		info := domain.GPUInfo{Path: "card1"}
		opener.EXPECT().Open(info).Return(next, nil).Once()
		clock := portsmocks.NewMockClock(t)
		clock.EXPECT().Now().Return(time.Now()).Once()
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().Stop().Return(false).Twice()
		run := outputsRun{ctx: ctx, b: &Backend{render: old, renderInfo: info, opener: opener},
			ch: ch, set: set, virtuals: host, stopping: map[string]bool{},
			apply: core.NewOutputApply(core.NewOutputOverrides(domain.Config{}, false)),
			log:   log, appLog: log, timer: timer, progress: &core.ApplyProgress{}}
		run.policy.ReopenLimiter.Clock = clock
		run.beginRecovery(ports.ErrDeviceLost)
		synctest.Wait()
		if _, ok := run.stopping["tv"]; !ok {
			t.Fatal("recovery did not track virtual stop")
		}
		if err, exit := run.onStopped(routing.Stopped{Name: "tv", Instance: instance}); err != nil || exit {
			t.Fatalf("recovery: %v %v", err, exit)
		}
		if run.policy.Recovering || len(run.stopping) != 0 {
			t.Fatalf("recovery retained stopping outputs: %v", run.stopping)
		}
		// No physical heads are required: the following apply must complete at once,
		// without arming its timeout for the already joined virtual lifetime.
		run.configure(domain.Config{})
		if run.progress.Active || run.deadline != nil {
			t.Fatal("following apply is waiting")
		}
		time.Sleep(applyTimeout)
		synctest.Wait()
		if run.progress.Active || run.deadline != nil {
			t.Fatal("following apply reached timeout")
		}
	})
}
