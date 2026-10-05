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
)

func TestSecurityProtectedStartupBlackProofAndCaptureRejection(t *testing.T) {
	o, k, commits, mu := testOutputMu(t)
	o.cursor = nil
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 2)
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
	r.EXPECT().Render(ports.Scene{Background: "#000000"}, map[ports.WindowID]ports.SurfaceContent(nil)).Return(nil, nil).Times(3)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	captures := make(chan ports.CaptureRequest, 1)
	captured := make(chan ports.CaptureDone, 1)
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{Seq: 99} // never render stale desktop after startup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, scenes, nil, nil, nil, captures, captured)
	}()
	select {
	case ev := <-events:
		if p, ok := ev.(ports.SecurityOutputProof); !ok || p.Proof.Generation != 1 || p.Proof.Instance != 77 || p.Proof.Kind != ports.ProtectionProtectedFrame {
			t.Fatalf("proof %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no startup black proof")
	}
	mu.Lock()
	for i, c := range *commits {
		if c.flags&atomicTestOnly != 0 {
			continue
		}
		active, _ := c.req.value(tCrtc, pActive)
		if active != 0 && i != len(*commits)-1 {
			t.Error("startup exposed an active target before protected transaction")
		}
	}
	mu.Unlock()
	f, w := protectionFence(t, false)
	w.Close()
	captures <- ports.CaptureRequest{ID: 9, Dst: ports.SHMBuffer{File: f}}
	select {
	case reply := <-captured:
		if reply.ID != 9 || reply.Err == nil {
			t.Fatalf("capture result %+v", reply)
		}
	case <-ctx.Done():
		t.Fatal("protected capture not failed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSecurityStartupClearWaitUsesRunContext(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w := protectionFence(t, false)
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
	r.EXPECT().UseTarget(0).Return().Once()
	fence, _ := protectionFence(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		cancel()
		return fence, nil
	}).Once()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	err := o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation: %v", err)
	}
	if _, err := fence.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("startup fence leaked")
	}
}

func TestSecurityWiredUnlockedCloseNeverRestoresSavedFB(t *testing.T) {
	o, k, commits := testOutput(t)
	var state atomic.Uint64
	securityGate(t, o, &state)
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close() // no protected snapshot needed: saved restoration is never admitted
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor)
}
