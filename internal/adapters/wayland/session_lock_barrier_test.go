package wayland

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// Real server fixture: channels are wired before Run, so the completion loop
// cannot latch a nil channel. No backend or protocol handler is replaced.
type barrierServer struct {
	s        *Server
	gate     *sessionsecurity.Gate
	changes  chan ports.SecurityState
	captures chan ports.CaptureRequest
	captured chan ports.CaptureDone
	cancel   context.CancelFunc
	stopped  chan struct{}
	dir      string
}

func newBarrierServer(t *testing.T) *barrierServer {
	t.Helper()
	h := &barrierServer{gate: &sessionsecurity.Gate{}, changes: make(chan ports.SecurityState, 1), captures: make(chan ports.CaptureRequest, 4), captured: make(chan ports.CaptureDone, 4), stopped: make(chan struct{}), dir: t.TempDir()}
	layout := ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 2, Height: 2}, Width: 2, Height: 2, Scale: 1}}
	var err error
	h.s, err = New(Options{RuntimeDir: h.dir, Outputs: layout, Security: h.gate, CaptureAllow: allowStore(t, "*\n")}, Channels{SecurityChanges: h.changes, SecurityEvents: make(chan ports.SecurityBackendEvent, 16), Captures: h.captures, Captured: h.captured}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	var runErr error
	go func() { runErr = h.s.Run(ctx); close(h.stopped) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.stopped:
			if runErr != nil {
				t.Error(runErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("barrier server shutdown blocked")
		}
	})
	return h
}

func TestSessionLockBarrierWaitsForAdmittedCopyAfterFrameDeath(t *testing.T) {
	h := newBarrierServer(t)
	// Reuse the real raw-wire admission client, retaining its SHM memfd so a
	// write performed after lock admission can be observed independently.
	client := protocolClient(t, h.s, h.dir)
	output := bindVersion(t, client, "wl_output", 4)
	registerProtocol(t, client, output)
	_, buffer, fd := captureSmallBuffer(t, client)
	defer unix.Close(fd)
	a := &admissionClient{c: client, wlr: bindVersion(t, client, "zwlr_screencopy_manager_v1", 3), output: output, buf: buffer}
	frame := a.captureWlr(t)
	var request ports.CaptureRequest
	select {
	case request = <-h.captures:
	case <-time.After(2 * time.Second):
		t.Fatal("actual capture not admitted")
	}
	// Pipeline owns the dup'd destination from this point, including closure.
	renderer := portsmocks.NewMockRenderer(t)
	pixels := portsmocks.NewMockCaptureFrame(t)
	entered, release := make(chan struct{}), make(chan struct{})
	pixels.EXPECT().Done().Return(nil)
	pixels.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error {
		close(entered)
		<-release
		copy(dst, []byte{0x42, 0x31, 0x20, 0xff})
		return nil
	}).Once()
	renderer.EXPECT().BeginCapture().Return(pixels, nil).Once()
	renderer.EXPECT().EndCapture(pixels).Return().Once()
	pipelineReplies := make(chan ports.CaptureDone, 1)
	pipeline := capture.NewPipeline(context.Background(), pipelineReplies)
	pipeline.Security = h.gate
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		pipeline.Close(renderer)
		// testify records the mmap-backed Read argument. Forget its call
		// history after joining the worker: expectation counters still verify
		// Once, but cleanup must not format bytes that are now unmapped.
		pixels.Calls = nil // worker joined; follows capture/pipeline_test.go
	})
	pipeline.SubmitScoped(h.gate.Snapshot(), renderer, []ports.CaptureRequest{request})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("capture worker did not enter Read")
	}
	a.destroy(t, frame)
	locker := protocolClient(t, h.s, h.dir)
	_, locked := lifecycleAcquire(t, locker)
	state := lifecycleTransition(t, h.changes)
	h.s.display.Do(func() {
		h.s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 1, Output: "HEADLESS-1"})
		h.s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
		lifecycleProof(h.s, state, 1)
		if len(h.s.captureInflight) != 1 || h.s.sessionLock.locked {
			t.Error("destroyed resource bypassed held copy barrier")
		}
	})
	roundtrip(t, locker)
	if *locked != 0 {
		t.Fatal("locked while Read can still write normal pixels")
	}
	close(release)
	released = true
	var completion ports.CaptureDone
	select {
	case completion = <-pipelineReplies:
	case <-time.After(2 * time.Second):
		t.Fatal("capture pipeline did not complete")
	}
	if completion.Err != nil || completion.ID != request.ID {
		t.Fatalf("capture completion %+v", completion)
	}
	if _, err := request.Dst.File.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("completion precedes destination close: %v", err)
	}
	got := make([]byte, 4)
	if _, err := unix.Pread(fd, got, 0); err != nil {
		t.Fatal(err)
	}
	if got[0] != 0x42 || got[3] != 0xff {
		t.Fatalf("actual copy not visible: %x", got)
	}
	h.s.display.Do(func() {
		if len(h.s.captureInflight) != 1 || h.s.sessionLock.locked {
			t.Error("worker completion not forwarded yet must retain credit")
		}
	})
	// Reply channel is the sole production completion funnel. Closing a frame
	// never synthesizes this reply or cancels its outstanding memory write.
	processed := make(chan struct{})
	h.s.display.Do(func() {
		original := h.s.captureReplies[request.ID]
		h.s.captureReplies[request.ID] = func(done ports.CaptureDone) {
			if original != nil {
				original(done)
			} // frame destruction removes the wire reply, not credit
			close(processed)
		}
	})
	h.captured <- completion
	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not process actual capture completion")
	}
	h.s.display.Do(func() {
		if len(h.s.captureInflight) != 0 || !h.s.sessionLock.locked {
			t.Error("completed copy did not clear lock barrier")
		}
	})
	// The opaque completed token returns the real capture lease on its owner.
	select {
	case token := <-pipeline.Completed():
		pipeline.Recycle(token, renderer)
	case <-time.After(2 * time.Second):
		t.Fatal("capture lease not returned")
	}
	roundtrip(t, locker)
	if *locked != 1 {
		t.Fatalf("locked events=%d", *locked)
	}
}

func TestSessionLockBarrierTakeoverSaturationRefusesWithoutGenerationChange(t *testing.T) {
	h := newBarrierServer(t)
	sentinel := ports.SecurityState{Generation: 999}
	h.changes <- sentinel // authoritative backend stalled for the whole churn
	for i := 0; i < maxSecurityPending; i++ {
		client := protocolClient(t, h.s, h.dir)
		_, locked := lifecycleAcquire(t, client)
		if *locked != 0 {
			t.Fatal("stalled backend confirmed lock")
		}
		orphaned := make(chan struct{})
		h.s.display.Do(func() { r := h.s.sessionLock.res; old := r.OnDestroy; r.OnDestroy = func() { old(); close(orphaned) } })
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-orphaned:
		case <-time.After(2 * time.Second):
			t.Fatal("owner death not processed")
		}
	}
	before := h.gate.Snapshot()
	client := protocolClient(t, h.s, h.dir)
	refused, err := bindLockManager(t, client).Lock()
	if err != nil {
		t.Fatal(err)
	}
	finished := 0
	refused.OnFinished(func() { finished++ })
	roundtrip(t, client)
	h.s.display.Do(func() {
		if len(h.s.securityPending) != maxSecurityPending || h.s.sessionLock != nil {
			t.Error("stalled outbox exceeded bound or admitted owner")
		}
	})
	if finished != 1 || h.gate.Snapshot() != before || !before.Protected {
		t.Fatalf("refusal finished=%d before=%+v after=%+v", finished, before, h.gate.Snapshot())
	}
}

func TestSessionLockBarrierReservedReleaseAndBlockedSenderShutdown(t *testing.T) {
	h := newBarrierServer(t)
	sentinel := ports.SecurityState{Generation: 999}
	h.changes <- sentinel
	client := protocolClient(t, h.s, h.dir)
	lock, locked := lifecycleAcquire(t, client)
	state := h.gate.Snapshot()
	var queued []ports.SecurityState
	h.s.display.Do(func() {
		// Immutable channel messages exercise the reserved release slot without
		// fabricating another accepted owner while this one is live.
		for len(h.s.securityPending) < maxSecurityPending {
			h.s.requestBackendSecurity(ports.SecurityState{Generation: ports.LockGeneration(100 + len(h.s.securityPending)), Protected: true})
		}
		queued = append(queued, h.s.securityPending...)
		h.s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
	})
	roundtrip(t, client)
	if *locked != 1 {
		t.Fatal("owner not confirmed")
	}
	if err := lock.UnlockAndDestroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, client)
	released := h.gate.Snapshot()
	h.s.display.Do(func() {
		if len(h.s.securityPending) != maxSecurityPending+1 || h.s.securityPending[maxSecurityPending] != released {
			t.Error("reserved release slot lost")
		}
	})
	if got := lifecycleTransition(t, h.changes); got != sentinel {
		t.Fatal("backend sentinel changed")
	}
	for _, want := range append(queued, released) {
		if got := lifecycleTransition(t, h.changes); got != want {
			t.Fatalf("outbox got %+v want %+v", got, want)
		}
	}
	// Block another transition and cancel the real server. Run joins the sender;
	// successful return proves it did not leak in a blocked channel send.
	h.changes <- sentinel
	h.s.display.Do(func() { h.s.requestBackendSecurity(ports.SecurityState{Generation: 777, Protected: true}) })
	h.cancel()
	select {
	case <-h.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked backend sender prevented Run shutdown")
	}
}
