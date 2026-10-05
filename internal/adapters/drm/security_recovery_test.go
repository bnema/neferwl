package drm

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// A missing flip event is recoverable only after the real acquire fence
// signals. Recovery revokes earlier proof before a fresh blocking black proof.
func TestSecurityStuckRecoveryInvalidatesBeforeFreshProof(t *testing.T) {
	o, k, commits, mu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	o.frame.stuckAfter = 40 * time.Millisecond
	o.flipped = make(chan flipEvent) // the event is intentionally lost
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 8)
	o.SecurityEvents = events
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w := protectionFence(t, false)
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	fence, signal := protectionFence(t, false)
	var renderedOnce atomic.Bool
	rendered := make(chan struct{}, 1)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Seq == 12 {
			if renderedOnce.CompareAndSwap(false, true) {
				select {
				case rendered <- struct{}{}:
				default:
				}
				return fence, nil
			}
			return nil, nil
		}
		if s.Background != "#000000" || s.Seq != 0 || len(s.Windows) != 0 {
			t.Error("recovery did not compose exact black")
		}
		return nil, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scenes := make(chan ports.Scene, 1)
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, scenes, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	receive := func() ports.SecurityBackendEvent {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-ctx.Done():
			t.Fatal("missing recovery security event")
			return nil
		}
	}
	initial := receive()
	if p, ok := initial.(ports.SecurityOutputProof); !ok || p.Proof != (ports.OutputProtection{Generation: 1, Instance: 77, Kind: ports.ProtectionProtectedFrame}) {
		t.Fatalf("initial proof %+v", initial)
	}
	scenes <- ports.Scene{Seq: 12, Security: ports.SecurityState{Generation: 1, Protected: true}}
	select {
	case <-rendered:
	case <-ctx.Done():
		t.Fatal("protected frame not submitted")
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range *commits {
			if c.user&3 == userFrame {
				return true
			}
		}
		return false
	})
	select {
	case event := <-events:
		t.Fatalf("unsignalled acquire fence forged recovery evidence: %+v", event)
	case <-time.After(3 * o.frame.stuckAfter):
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if event := receive(); event != (ports.SecurityOutputInvalidated{Generation: 1, Instance: 77}) {
		t.Fatalf("fresh proof preceded recovery invalidation: %+v", event)
	}
	if event := receive(); event != initial {
		t.Fatalf("fresh recovery proof changed lifetime/epoch: %+v", event)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var frameIndex int
	for i, c := range *commits {
		if c.user&3 == userFrame {
			frameIndex = i
			break
		}
	}
	if frameIndex == 0 || len(*commits) < frameIndex+3 {
		t.Fatal("no actual blocking black recovery transaction")
	}
	checkProtectedDisable(t, (*commits)[frameIndex+1], tPrimary)
	activate := (*commits)[frameIndex+2]
	if active, ok := activate.req.value(tCrtc, pActive); !ok || active != 1 || activate.flags != atomicAllowModes {
		t.Fatal("recovery proof did not follow blocking activation")
	}
}
