package capture

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestRequestsAdmitRefuseAndFull(t *testing.T) {
	replies := make(chan ports.CaptureDone, MaxRequests+2)
	var r Requests
	r.Init(context.Background(), replies)
	scene := ports.Scene{OutputWidth: 10, OutputHeight: 10}

	require.False(t, r.Admit(ports.CaptureRequest{ID: 1}, ErrOutputOff, scene, true))
	require.ErrorIs(t, (<-replies).Err, ErrOutputOff)
	require.Zero(t, r.Len())

	// A request without an indicator is ready at once on a known scene.
	require.True(t, r.Admit(ports.CaptureRequest{ID: 2}, nil, scene, true))
	require.False(t, r.Admit(ports.CaptureRequest{ID: 3}, nil, scene, false), "no scene yet")
	require.False(t, r.List[0].Since.IsZero())
	for i := r.Len(); i < MaxRequests; i++ {
		r.Admit(ports.CaptureRequest{ID: uint64(10 + i)}, nil, scene, true)
	}
	require.False(t, r.Admit(ports.CaptureRequest{ID: 99}, nil, scene, true))
	done := <-replies
	require.Equal(t, uint64(99), done.ID)
	require.ErrorIs(t, done.Err, ErrBatchFull)
	require.Equal(t, MaxRequests, r.Len())
}

func TestRequestsFailPendingSkipsHanded(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	var r Requests
	r.Init(context.Background(), replies)
	r.Admit(ports.CaptureRequest{ID: 1}, nil, ports.Scene{}, false)
	r.Admit(ports.CaptureRequest{ID: 2}, nil, ports.Scene{}, false)
	r.List[0] = ports.CaptureRequest{} // handed to the worker
	r.FailPending(ErrEpochChanged)
	require.Zero(t, r.Len())
	require.Len(t, replies, 1)
	done := <-replies
	require.Equal(t, uint64(2), done.ID)
	require.ErrorIs(t, done.Err, ErrEpochChanged)
}

func TestRequestsStopDrainsIncoming(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	var r Requests
	r.Init(context.Background(), replies)
	r.Admit(ports.CaptureRequest{ID: 1}, nil, ports.Scene{}, false)
	r.Admit(ports.CaptureRequest{ID: 3}, nil, ports.Scene{}, false)
	r.List[1] = ports.CaptureRequest{} // handed to the pipeline
	incoming := make(chan ports.CaptureRequest, 2)
	incoming <- ports.CaptureRequest{ID: 2}
	close(incoming)
	r.Stop(incoming)
	require.Len(t, replies, 2)
	for _, id := range []uint64{1, 2} {
		done := <-replies
		require.Equal(t, id, done.ID)
		require.ErrorIs(t, done.Err, ErrOutputStopped)
	}
}

func TestRequestsStopReturnsOnOpenEmptyChannel(t *testing.T) {
	replies := make(chan ports.CaptureDone, 1)
	var r Requests
	r.Init(context.Background(), replies)
	r.Stop(make(chan ports.CaptureRequest))
	require.Empty(t, replies)
}
