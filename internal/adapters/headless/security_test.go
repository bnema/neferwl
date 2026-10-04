package headless

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func securityReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("timed out")
		var z T
		return z
	}
}

func TestSecurityInitialBlackFenceBeforeLockerAndNoReadback(t *testing.T) {
	state := ports.SecurityState{Generation: 7, Protected: true}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().Return(state)
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	fence, signal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer signal.Close()
	rendered := make(chan ports.Scene, 4)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Seq == 0 {
			if s.Background != "#000000" || s.Security != state || len(s.Windows) != 0 || len(s.Layers) != 0 || s.Capture != nil || s.CaptureScene != nil || c != nil {
				t.Errorf("unsafe initial black: %+v surfaces %v", s, c)
			}
			rendered <- s
			return fence, nil
		}
		if s.Security != state || s.Seq != 2 {
			t.Errorf("stale desktop rendered: %+v", s)
		}
		rendered <- s
		return nil, nil
	}).Twice()
	// No Pixels/BeginCapture expectation: either call fails this test.
	scenes := make(chan ports.Scene, 3)
	scenes <- ports.Scene{Seq: 1, Windows: []ports.SceneWindow{{ID: 1}}}
	scenes <- ports.Scene{Security: state, Seq: 2, Background: "#000000", Windows: []ports.SceneWindow{{ID: 10, Fullscreen: true, Rect: ports.Rect{W: 2, H: 2}}}}
	contents := make(chan ports.SurfaceContent, 1)
	acquire, acquireSignal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer acquire.Close()
	defer acquireSignal.Close()
	contents <- ports.SurfaceContent{ID: 10, SHM: &ports.SHMBuffer{Pool: 10}, Acquire: acquire}
	proof := make(chan ports.SecurityBackendEvent, 4)
	replies := make(chan ports.CaptureDone, 4)
	requests := make(chan ports.CaptureRequest, 2)
	dst, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	requests <- ports.CaptureRequest{ID: 44, Dst: ports.SHMBuffer{File: dst}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		done <- Run(ctx, Options{Security: gate, Instance: 9, SecurityEvents: proof, Width: 2, Height: 2, Name: "H", ScreenshotDir: dir, Cursor: &Cursor{}, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents, nil, requests)
	}()
	t.Cleanup(func() {
		cancel()
		if err := securityReceive(t, done); err != nil {
			t.Error(err)
		}
	})
	securityReceive(t, rendered)
	select {
	case p := <-proof:
		t.Fatalf("proof before fence: %+v", p)
	default:
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	p := securityReceive(t, proof).(ports.SecurityOutputProof)
	if p.Proof != (ports.OutputProtection{Generation: 7, Instance: 9, Kind: ports.ProtectionProtectedFrame}) {
		t.Fatal(p)
	}
	securityReceive(t, rendered)
	result := securityReceive(t, replies)
	if result.ID != 44 || result.Err == nil {
		t.Fatal(result)
	}
	if _, err := dst.Stat(); err == nil {
		t.Fatal("capture destination not closed")
	}
	if err := dst.Close(); err == nil {
		t.Fatal("destination was still open")
	}
	select {
	case result := <-replies:
		t.Fatalf("duplicate failure: %+v", result)
	default:
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("protected screenshot files: %v %v", files, err)
	}
}

func TestSecurityChangesWakeIdleAndRejectQueuedDesktop(t *testing.T) {
	var state atomic.Value
	state.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state.Load().(ports.SecurityState) })
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	rendered := make(chan ports.Scene, 4)
	desktopFinished := make(chan struct{})
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Seq != 0 {
			rendered <- s
			<-desktopFinished
			return nil, nil
		}
		if len(s.Windows) != 0 || c != nil || !s.Security.Protected {
			t.Errorf("bad transition black %+v", s)
		}
		rendered <- s
		return nil, nil
	}).Twice()
	scenes := make(chan ports.Scene, 2)
	scenes <- ports.Scene{Seq: 1, Windows: []ports.SceneWindow{{ID: 1}}}
	changes := make(chan ports.SecurityState, 1)
	proofs := make(chan ports.SecurityBackendEvent, 2)
	presented := make(chan ports.OutputPresented, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, SecurityChanges: changes, Instance: 3, SecurityEvents: proofs, Presented: presented, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil)
	}()
	t.Cleanup(func() {
		cancel()
		if err := securityReceive(t, done); err != nil {
			t.Error(err)
		}
	})
	securityReceive(t, rendered)
	locked := ports.SecurityState{Generation: 1, Protected: true}
	state.Store(locked)
	scenes <- ports.Scene{Seq: 99, Windows: []ports.SceneWindow{{ID: 1}}}
	changes <- locked
	close(desktopFinished)
	s := securityReceive(t, rendered)
	if s.Security != locked || s.Seq != 0 {
		t.Fatal(s)
	}
	securityReceive(t, proofs)
	select {
	case p := <-presented:
		if p.Flip != nil {
			t.Fatalf("stale render reported: %+v", p)
		}
	default:
	}
}

func TestSecurityDedicatedChangeWakesWithoutScene(t *testing.T) {
	var state atomic.Value
	state.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state.Load().(ports.SecurityState) })
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	changes := make(chan ports.SecurityState, 1)
	proof := make(chan ports.SecurityBackendEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, SecurityChanges: changes, Instance: 5, SecurityEvents: proof, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, nil)
	}()
	locked := ports.SecurityState{Generation: 2, Protected: true}
	state.Store(locked)
	changes <- locked
	securityReceive(t, proof)
	cancel()
	if err := securityReceive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityCursorAndPNGSuppressed(t *testing.T) {
	gate := portsmocks.NewMockSessionSecurity(t)
	state := ports.SecurityState{Generation: 1, Protected: true}
	gate.EXPECT().Snapshot().Return(state)
	c := &Cursor{}
	c.set(ports.CursorImage{W: 1, H: 1, Pixels: []byte{0, 0, 255, 255}})
	c.Move(0, 0)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	c.drawSecure(img, gate)
	if img.Pix[0] != 0 {
		t.Fatal("protected cursor drawn")
	}
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := writePNGSecure(path, img, gate, ports.SecurityState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale screenshot delivered")
	}
}

// The raw HDR file follows the screenshot security gating: nothing is read
// back or written for a protected session, none is read back once the
// session turned protected after latest.png, and a frame that crossed a
// transition during the readback is not written.
func TestSecurityRawHDRSuppressed(t *testing.T) {
	unprotected := ports.SecurityState{Generation: 1}
	protected := ports.SecurityState{Generation: 2, Protected: true}
	tests := []struct {
		name string
		// hdrPixels: HDRPixels is expected and flips the session protected.
		hdrPixels bool
		// flipAfterLatest: the session turns protected once latest.png exists.
		flipAfterLatest bool
		startProtected  bool
		wantLatest      bool
	}{
		{name: "protected from the start", startProtected: true},
		{name: "protected after latest.png, before the readback", flipAfterLatest: true, wantLatest: true},
		{name: "protected during the readback", hdrPixels: true, wantLatest: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var state atomic.Value
			state.Store(unprotected)
			if tc.startProtected {
				state.Store(protected)
			}
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
				if tc.flipAfterLatest {
					if _, err := os.Stat(filepath.Join(dir, "latest.png")); err == nil {
						state.Store(protected)
					}
				}
				return state.Load().(ports.SecurityState)
			})
			f, err := os.CreateTemp(t.TempDir(), "target")
			if err != nil {
				t.Fatal(err)
			}
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().SetHDR(float64(203)).Return().Once()
			r.EXPECT().ExportTargets(1, []uint64(nil)).Return([]ports.DMABuf{{Planes: []ports.DMABufPlane{{File: f}}}}, nil).Once()
			rendered := make(chan ports.Scene, 4)
			r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
				rendered <- s
				return nil, nil
			}).Maybe()
			if !tc.startProtected {
				r.EXPECT().Pixels().Return(image.NewRGBA(image.Rect(0, 0, 2, 2))).Maybe()
			}
			// Without an HDRPixels expectation, a readback fails the test.
			if tc.hdrPixels {
				r.EXPECT().HDRPixels().RunAndReturn(func() *image.RGBA64 {
					state.Store(protected)
					return image.NewRGBA64(image.Rect(0, 0, 2, 2))
				}).Once()
			}
			r.EXPECT().Close().Return().Once()
			scenes := make(chan ports.Scene, 1)
			scenes <- ports.Scene{Seq: 1, Security: unprotected, OutputWidth: 2, OutputHeight: 2}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{Security: gate, Width: 2, Height: 2, HDR: true, RawHDR: true, ScreenshotDir: dir, Name: "H", Log: logging.For(ctx, "render"), NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil)
			}()
			// The black protection frame follows the frame under test (or is
			// the only one): the raw block is over once it is rendered.
			for s := securityReceive(t, rendered); !s.Security.Protected; s = securityReceive(t, rendered) {
			}
			cancel()
			if err := securityReceive(t, done); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "latest.png")); (err == nil) != tc.wantLatest {
				t.Errorf("latest.png: %v, want present %v", err, tc.wantLatest)
			}
			if _, err := os.Stat(filepath.Join(dir, "latest-pq.png")); !os.IsNotExist(err) {
				t.Errorf("latest-pq.png delivered: %v", err)
			}
		})
	}
	// The write gate itself refuses a stale or protected 16-bit image.
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().Return(protected)
	path := filepath.Join(t.TempDir(), "latest-pq.png")
	if err := writePNGSecure(path, image.NewRGBA64(image.Rect(0, 0, 1, 1)), gate, ports.SecurityState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale raw screenshot delivered")
	}
}

func TestSecurityQueuedCaptureFailedOnceAndProofCancellation(t *testing.T) {
	var state atomic.Value
	state.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state.Load().(ports.SecurityState) })
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	black := make(chan struct{})
	r.EXPECT().Render(mock.Anything, mock.Anything).Run(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) { close(black) }).Return(nil, nil).Once()
	incoming := make(chan ports.CaptureRequest)
	replies := make(chan ports.CaptureDone, 2)
	changes := make(chan ports.SecurityState, 1)
	// No receiver: proof delivery must wait rather than drop, and cancel.
	proofs := make(chan ports.SecurityBackendEvent)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, SecurityChanges: changes, SecurityEvents: proofs, Instance: 4, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, incoming)
	}()
	file, err := os.CreateTemp(t.TempDir(), "queued")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case incoming <- ports.CaptureRequest{ID: 100, Dst: ports.SHMBuffer{File: file}}:
	case <-time.After(time.Second):
		t.Fatal("capture not admitted")
	}
	locked := ports.SecurityState{Generation: 3, Protected: true}
	state.Store(locked)
	changes <- locked
	securityReceive(t, black)
	cancel()
	if err := securityReceive(t, done); err != nil {
		t.Fatal(err)
	}
	result := securityReceive(t, replies)
	if result.ID != 100 || result.Err == nil {
		t.Fatal(result)
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("queued FD leaked")
	}
	select {
	case result := <-replies:
		t.Fatalf("duplicate queued reply: %+v", result)
	default:
	}
}

func TestSecurityOffOutputProofDoesNotRender(t *testing.T) {
	var state atomic.Value
	state.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state.Load().(ports.SecurityState) })
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{Off: true}
	presented := make(chan ports.OutputPresented, 1)
	changes := make(chan ports.SecurityState, 1)
	proofs := make(chan ports.SecurityBackendEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, SecurityChanges: changes, SecurityEvents: proofs, Instance: 12, Presented: presented, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil)
	}()
	securityReceive(t, presented) // Off has actually been applied by the owner.
	locked := ports.SecurityState{Generation: 4, Protected: true}
	state.Store(locked)
	changes <- locked
	proof := securityReceive(t, proofs).(ports.SecurityOutputProof)
	if proof.Proof.Kind != ports.ProtectionInactiveOutput || proof.Proof.Generation != 4 || proof.Proof.Instance != 12 {
		t.Fatal(proof)
	}
	cancel()
	if err := securityReceive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityCompletionLeaseRecycledBeforeProtectionRenderFailure(t *testing.T) {
	var state atomic.Value
	state.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	var submitted atomic.Bool
	ownerWaiting := make(chan struct{})
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
		v := state.Load().(ports.SecurityState)
		if submitted.CompareAndSwap(true, false) {
			close(ownerWaiting)
		}
		return v
	})
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).RunAndReturn(func(image.Rectangle, []byte, int) error {
		<-ownerWaiting // owner has checked the old gate and is selecting
		state.Store(ports.SecurityState{Generation: 1, Protected: true})
		return nil
	}).Once()
	r.EXPECT().BeginCapture().Run(func() { submitted.Store(true) }).Return(frame, nil).Once()
	var recycled atomic.Bool
	r.EXPECT().EndCapture(frame).Run(func(ports.CaptureFrame) { recycled.Store(true) }).Return().Once()
	failure := errors.New("protection render failed")
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Seq == 0 {
			if !recycled.Load() {
				t.Error("pulled completion lease not recycled before transition")
			}
			return nil, failure
		}
		return nil, nil
	}).Twice()
	r.EXPECT().Close().Return().Once()
	incoming := make(chan ports.CaptureRequest)
	scenes := make(chan ports.Scene)
	replies := make(chan ports.CaptureDone, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, Width: 2, Height: 2, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, incoming)
	}()
	file, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(16); err != nil {
		t.Fatal(err)
	}
	incoming <- ports.CaptureRequest{ID: 77, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: ports.SHMBuffer{File: file}}
	scenes <- ports.Scene{Seq: 1, Scale: 1}
	securityReceive(t, replies)
	if err := securityReceive(t, done); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !recycled.Load() {
		t.Fatal("capture lease leaked")
	}
	// Read arguments contain an mmap slice that capture has now unmapped;
	// don't let testify format that expired borrowed memory at cleanup.
	frame.Calls = nil
}
