package headlessbackend

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestVirtualReloadInventory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}}
		apply := core.NewOutputApply(core.NewOutputOverrides(file, true))
		events := make(chan ports.OutputEvent, 16)
		heads := make(chan domain.OutputHeads, 16)
		reload := make(chan domain.ConfigChanged)
		ch := routing.Channels{Events: events, Heads: heads, Reloads: reload}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Twice()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{Sizes: [][2]int{{20, 20}}}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		inventory := <-heads
		if len(inventory.Heads) != 2 || inventory.Heads[1].Info.Name != "tv" || !inventory.Heads[1].Info.Virtual {
			t.Fatal(inventory)
		}
		// Inventory is published before owners necessarily pass their announced
		// gates. Let both create their renderers before testing removal, otherwise
		// tv may correctly stop without ever owning a renderer to close.
		synctest.Wait()
		reload <- domain.ConfigChanged{Config: domain.Config{}}
		inventory = <-heads
		if len(inventory.Heads) != 1 {
			t.Fatal(inventory)
		}
		synctest.Wait()
		found := false
		for len(events) > 0 {
			if removed, ok := (<-events).(ports.OutputRemoved); ok && removed.Name == "tv" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing removal")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
