package routing

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestCapturedDemux(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in := make(chan domain.CaptureDone, 4)
		wl := make(chan domain.CaptureDone, 4)
		internal := make(chan domain.CaptureDone, 4)
		control := make(chan domain.CaptureDone, 4)
		exited := make(chan struct{})
		go func() { RouteCaptured(ctx, in, wl, internal, control); close(exited) }()

		in <- domain.CaptureDone{ID: ports.CaptureIDControl | 5}
		in <- domain.CaptureDone{ID: 7}
		in <- domain.CaptureDone{ID: ports.CaptureIDInternal | 3}
		require.Equal(t, uint64(7), recvDone(t, wl).ID)
		require.Equal(t, ports.CaptureIDInternal|3, recvDone(t, internal).ID)
		require.Equal(t, ports.CaptureIDControl|5, recvDone(t, control).ID)
		require.Empty(t, control)
		require.Empty(t, wl)
		require.Empty(t, internal)

		cancel()
		synctest.Wait()
		select {
		case <-exited:
		default:
			t.Fatal("routeCaptured did not return on cancel")
		}

	})
}

func TestCapturedDemuxCancelWhileBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		in := make(chan domain.CaptureDone, 1)
		wl := make(chan domain.CaptureDone) // never read
		exited := make(chan struct{})
		go func() { RouteCaptured(ctx, in, wl, nil, nil); close(exited) }()
		in <- domain.CaptureDone{ID: 1}
		cancel()
		synctest.Wait()
		select {
		case <-exited:
		default:
			t.Fatal("routeCaptured blocked on a full destination after cancel")
		}

	})
}

func TestCapturedDemuxDropsInternalWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in := make(chan domain.CaptureDone, 2)
		wl := make(chan domain.CaptureDone, 2)
		go RouteCaptured(ctx, in, wl, nil, nil)
		in <- domain.CaptureDone{ID: ports.CaptureIDInternal | 1}
		in <- domain.CaptureDone{ID: ports.CaptureIDControl | 1}
		in <- domain.CaptureDone{ID: 2}
		require.Equal(t, uint64(2), recvDone(t, wl).ID)

	})
}

func recvDone(t *testing.T, ch <-chan domain.CaptureDone) domain.CaptureDone {
	t.Helper()
	synctest.Wait()
	select {
	case d := <-ch:
		return d
	default:
		t.Fatal("no capture reply")
		return domain.CaptureDone{}
	}
}

func TestControlCaptureRejectsProtectedAndChangedEpochBeforeRouting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, state := range []domain.SecurityState{{Generation: 4}, {Generation: 3, Protected: true}} {
			in := make(chan domain.CaptureDone, 1)
			wl := make(chan domain.CaptureDone, 1)
			internal := make(chan domain.CaptureDone, 1)
			control := make(chan domain.CaptureDone, 1)
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().Return(state)
			renderer := portsmocks.NewMockRenderer(t) // BeginCapture must never be called.
			ctx, cancel := context.WithCancel(context.Background())
			pipeline := capture.NewPipeline(ctx, in)
			pipeline.Security = gate
			id := ports.CaptureIDControl | 1
			pipeline.SubmitScoped(domain.SecurityState{Generation: 3}, renderer, []domain.CaptureRequest{{ID: id, Mapped: make([]byte, 16), Width: 2, Height: 2, Stride: 8, Format: 1}})
			pipeline.Close(renderer)
			go RouteCaptured(ctx, in, wl, internal, control)
			got := recvDone(t, control)
			require.Equal(t, id, got.ID)
			require.ErrorIs(t, got.Err, capture.ErrSecurityState)
			require.Empty(t, wl)
			require.Empty(t, internal)
			cancel()
			synctest.Wait()
		}
	})
}
