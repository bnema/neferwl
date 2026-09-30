package drm

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestSecurityTransitionDuringDesktopRenderPreservesReadFence(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor, o.scanout = nil, false
	var state atomic.Uint64
	securityGate(t, o, &state)
	r := portsmocks.NewMockRenderer(t)
	f, _ := protectionFence(t, false)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		state.Store(3)
		return f, nil
	}).Once()
	_, err := o.submitFrame(context.Background(), r, ports.Scene{}, nil, nil, nil, nil)
	if !errors.Is(err, errSecurityScene) || len(*commits) != 0 || o.readDone() {
		t.Fatalf("old rendered frame committed or read fence dropped: %v", err)
	}
	o.dropRead()
}

func TestSecurityMasterLossInvalidatesWithoutProof(t *testing.T) {
	o, _, _ := testOutput(t)
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 2)
	o.SecurityEvents = events
	o.observeSecurity()
	o.securityPrepared = true
	enabled := true
	if !o.commitFailed(unix.EACCES, &enabled) || enabled || o.securityPrepared {
		t.Fatal("master loss retained proof")
	}
	if err := o.sendSecurityInvalid(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := <-events
	if invalid, ok := ev.(ports.SecurityOutputInvalidated); !ok || invalid.Generation != 1 || invalid.Instance != 77 {
		t.Fatalf("invalid event %+v", ev)
	}
	if len(events) != 0 {
		t.Fatal("master loss forged inactive proof")
	}
}

func TestSecurityProofBackpressureRechecksEpoch(t *testing.T) {
	o, k, _ := testOutput(t)
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent) // no consumer until transition
	o.SecurityEvents = events
	r := portsmocks.NewMockRenderer(t)
	cleared := make(chan struct{})
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	k.EXPECT().createBlob(mock.Anything).RunAndReturn(func([]byte) (uint32, error) { close(cleared); return 99, nil }).Once()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.prepareSecurity(ctx, r) }()
	<-cleared
	state.Store(5)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("obsolete proof emitted: %+v", ev)
	default:
	}
	if o.securityPrepared {
		t.Fatal("obsolete proof retained")
	}
}
