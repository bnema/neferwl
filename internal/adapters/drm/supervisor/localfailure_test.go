package supervisor

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/require"
)

func TestLocalImportFailureKeepsHealthySiblingRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		set := routing.NewSet(ctx, nil, routing.Routes{})
		defer func() { require.NoError(t, set.Wait()) }()
		stopped := make(chan struct{})
		require.True(t, set.Start(ctx, "DP-2", func(ctx context.Context, _ routing.Feed) error {
			<-ctx.Done()
			close(stopped)
			return nil
		}))
		dev := portsmocks.NewMockRenderDevice(t)
		copier := portsmocks.NewMockCopier(t)
		refusal := errors.New("unsupported source import")
		copier.EXPECT().ImportSources([]domain.DMABuf(nil)).Return(refusal).Once()
		card := &drmCard{local: &localRenderDevice{dev: dev}}
		run := &outputsRun{set: set, cards: map[string]*drmCard{"DP-1": card, "DP-2": card}, stopping: map[string]bool{}, progress: &core.ApplyProgress{}}
		err := copier.ImportSources(nil)
		run.stopLocalDeviceUsers(card, "DP-1", err)
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("import refusal stopped healthy sibling")
		default:
		}
		require.False(t, card.localLost)
		require.Same(t, dev, card.local.dev, "import refusal invalidated cached device")
		require.Empty(t, run.stopping)
	})
}
