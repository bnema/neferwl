package supervisor

import (
	"context"
	"testing"
	"testing/synctest"

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
		defer set.Wait()
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
		run.onSeat(false)
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
		defer set.Wait()
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
