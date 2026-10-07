package drm

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestSecurityRunWakePendingResumeAndOff(t *testing.T) {
	synctest.Test(t, securityRunWakePendingResumeAndOff)
}

func securityRunWakePendingResumeAndOff(t *testing.T) {
	o, k, commits, mu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	var state atomic.Uint64
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 8)
	o.SecurityEvents = events
	wake := make(chan ports.SecurityState, 1)
	o.SecurityChanges = wake
	active := make(chan bool, 2)
	flips := make(chan flipEvent, 2)
	o.flipped = flips
	scenes := make(chan ports.Scene, 2)
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w := protectionFence(t, false)
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	black := make(chan struct{}, 8)
	desktop := make(chan struct{}, 2)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Seq == 99 {
			desktop <- struct{}{}
		} else {
			black <- struct{}{}
		}
		return nil, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, scenes, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	scenes <- ports.Scene{Seq: 99, Scale: 1}
	select {
	case <-desktop:
	case <-ctx.Done():
		t.Fatal("no desktop frame")
	}
	var frame commitRec
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range *commits {
			if c.user&3 == userFrame {
				frame = c
				return true
			}
		}
		return false
	})
	state.Store(3)
	wake <- ports.SecurityState{} // stale payload: read gate, not payload
	synctest.Wait()
	select {
	case ev := <-events:
		t.Fatalf("proof while old frame pending: %+v", ev)
	default:
	}
	flips <- eventOf(frame)
	proof := func(kind ports.ProtectionKind) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if p, ok := ev.(ports.SecurityOutputProof); ok {
					if p.Proof != (ports.OutputProtection{Generation: 1, Instance: 77, Kind: kind}) {
						t.Fatalf("proof %+v", p)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal("missing security proof")
			}
		}
	}
	proof(ports.ProtectionProtectedFrame)
	scenes <- ports.Scene{Seq: 99} // held desktop scene cannot be replayed
	synctest.Wait()
	select {
	case <-desktop:
		t.Fatal("stale desktop rendered under protection")
	default:
	}
	active <- false
	select {
	case ev := <-events:
		if _, ok := ev.(ports.SecurityOutputInvalidated); !ok {
			t.Fatalf("disable did not invalidate before resume: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("missing disable invalidation")
	}
	active <- true
	select {
	case ev := <-events:
		if _, ok := ev.(ports.SecurityOutputInvalidated); !ok {
			t.Fatalf("resume proof preceded invalidation: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("missing resume invalidation")
	}
	proof(ports.ProtectionProtectedFrame) // reacquire requires a fresh black transaction
	scenes <- ports.Scene{Security: ports.SecurityState{Generation: 1, Protected: true}, Off: true}
	proof(ports.ProtectionInactiveOutput)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
