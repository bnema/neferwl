package drm

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestSecurityEngageDuringExcludeRenderRejectsCaptureTransfer(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	var state atomic.Uint64
	securityGate(t, o, &state)
	r := portsmocks.NewMockRenderer(t)
	f, _ := protectionFence(t, false)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		state.Store(3)
		return f, nil
	}).Once()
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	requests := []ports.CaptureRequest{sessionRequest(t, 1, true), sessionRequest(t, 2, false)}
	files := []*os.File{requests[0].Dst.File, requests[1].Dst.File}
	_, err := o.submitFrame(context.Background(), r, drmSessionScene(), nil, nil, requests, pipeline)
	if !errors.Is(err, errSecurityScene) || len(*commits) != 0 {
		t.Fatalf("capture admission result: %v", err)
	}
	if len(o.readFences) != 1 || o.readDone() {
		t.Fatal("rejected excluded render dropped GPU reads")
	}
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("returned excluded fence not closed")
	}
	// Match Run's remaining-credit rejection: nothing was transferred to the
	// worker, each request is failed once, and every owned FD is closed.
	for _, q := range requests {
		if capture.Handed(q) {
			t.Fatal("obsolete capture transferred credit")
		}
		capture.Fail(context.Background(), q, err, replies)
	}
	pipeline.Close(r)
	r.AssertNotCalled(t, "BeginCapture")
	if len(replies) != 2 {
		t.Fatalf("capture replies %d", len(replies))
	}
	for range 2 {
		if reply := <-replies; !errors.Is(reply.Err, errSecurityScene) {
			t.Fatalf("reply %+v", reply)
		}
	}
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("capture FD not closed")
		}
	}
	o.dropRead()
}

func TestSecurityHiddenAdmissionRechecksBeforeNativeStart(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().Return(ports.SecurityState{}).Once()
	security.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true})
	o.Security = security
	r := portsmocks.NewMockRenderer(t)
	child := portsmocks.NewMockRenderer(t)
	replies := make(chan ports.CaptureDone, 1)
	pipeline := capture.NewPipeline(context.Background(), replies)
	pipeline.EnableOffscreen(func(int, int) (ports.Renderer, error) {
		t.Fatal("obsolete hidden capture created child")
		return child, nil
	})
	s := drmSessionScene()
	s.CaptureScene = &ports.Scene{OutputWidth: 200, OutputHeight: 100, Scale: 1}
	requests := []ports.CaptureRequest{sessionRequest(t, 1, true)}
	file := requests[0].Dst.File
	_, err := o.submitFrame(context.Background(), r, s, nil, nil, requests, pipeline)
	if !errors.Is(err, errSecurityScene) || capture.Handed(requests[0]) {
		t.Fatalf("hidden admission result %v", err)
	}
	capture.Fail(context.Background(), requests[0], err, replies)
	pipeline.Close(r)
	if len(replies) != 1 || (<-replies).ID != 1 {
		t.Fatal("hidden request credit not failed once")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("hidden FD not closed")
	}
	r.AssertNotCalled(t, "BeginCapture")
	child.AssertNotCalled(t, "Render", mock.Anything, mock.Anything)
	child.AssertNotCalled(t, "BeginCapture")
}
