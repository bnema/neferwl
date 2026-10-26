package app

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCapturedDemux(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in := make(chan ports.CaptureDone, 4)
		wl := make(chan ports.CaptureDone, 4)
		internal := make(chan ports.CaptureDone, 4)
		exited := make(chan struct{})
		go func() { routeCaptured(ctx, in, wl, internal); close(exited) }()

		in <- ports.CaptureDone{ID: 7}
		in <- ports.CaptureDone{ID: ports.CaptureIDInternal | 3}
		require.Equal(t, uint64(7), recvDone(t, wl).ID)
		require.Equal(t, ports.CaptureIDInternal|3, recvDone(t, internal).ID)
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
		in := make(chan ports.CaptureDone, 1)
		wl := make(chan ports.CaptureDone) // never read
		exited := make(chan struct{})
		go func() { routeCaptured(ctx, in, wl, nil); close(exited) }()
		in <- ports.CaptureDone{ID: 1}
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
		in := make(chan ports.CaptureDone, 2)
		wl := make(chan ports.CaptureDone, 2)
		go routeCaptured(ctx, in, wl, nil)
		in <- ports.CaptureDone{ID: ports.CaptureIDInternal | 1}
		in <- ports.CaptureDone{ID: 2}
		require.Equal(t, uint64(2), recvDone(t, wl).ID)

	})
}

func recvDone(t *testing.T, ch <-chan ports.CaptureDone) ports.CaptureDone {
	t.Helper()
	synctest.Wait()
	select {
	case d := <-ch:
		return d
	default:
		t.Fatal("no capture reply")
		return ports.CaptureDone{}
	}
}
