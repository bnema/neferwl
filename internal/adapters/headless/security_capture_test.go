package headless

import (
	"context"
	"errors"
	"image"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// Change the gate AFTER the output's post-Render check returns the admitting
// state. SubmitScoped must close this last gap before native BeginCapture.
func TestScopedCaptureClosesPostRenderAdmissionGap(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		name := "normal"
		if excluded {
			name = "excluded"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				admitted := domain.SecurityState{Generation: 2}
				protected := domain.SecurityState{Generation: 3, Protected: true}
				var state atomic.Value
				state.Store(admitted)
				var arm atomic.Bool
				gate := portsmocks.NewMockSessionSecurity(t)
				gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
					before := state.Load().(domain.SecurityState)
					if arm.CompareAndSwap(true, false) {
						state.Store(protected)
					}
					return before
				})
				r := portsmocks.NewMockRenderer(t)
				r.EXPECT().Close().Return().Once()
				frameRendered := make(chan struct{}, 1)
				proof := make(chan ports.SecurityBackendEvent, 1)
				r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, _ map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
					if s.Security == protected {
						if s.Seq != 0 || len(s.Windows) != 0 {
							t.Errorf("bad protection frame %+v", s)
						}
						return nil, nil
					}
					if s.Security != admitted {
						t.Errorf("wrong render epoch %+v", s)
					}
					arm.Store(true)
					frameRendered <- struct{}{}
					return nil, nil
				}).Twice()
				// BeginCapture has no expectation: any native export fails the test.
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				scenes := make(chan domain.Scene, 1)
				incoming := make(chan domain.CaptureRequest)
				replies := make(chan domain.CaptureDone, 2)
				go func() {
					done <- Run(ctx, Options{Security: gate, SecurityEvents: proof, Instance: 8, Width: 2, Height: 2, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, incoming)
				}()
				dst := sessionCaptureFile(t)
				incoming <- domain.CaptureRequest{ID: 91, Exclude: excluded, Session: 7, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: dst}
				scene := domain.Scene{Security: admitted, Seq: 2, Scale: 1, OutputWidth: 2, OutputHeight: 2}
				if excluded {
					scene = sessionScene()
					scene.Security = admitted
					scene.Scale = 1
				}
				scenes <- scene
				securityReceive(t, frameRendered)
				result := securityReceive(t, replies)
				if result.ID != 91 || !errors.Is(result.Err, capture.ErrSecurityState) {
					t.Fatalf("scoped rejection missing: %+v", result)
				}
				if _, err := dst.File.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("destination not closed: %v", err)
				}
				securityReceive(t, proof)
				cancel()
				if err := securityReceive(t, done); err != nil {
					t.Fatal(err)
				}
				select {
				case extra := <-replies:
					t.Fatalf("duplicate reply %+v", extra)
				default:
				}
			})
		})
	}
}

func TestTransitionDuringHeadlessRenderWaitRetainsFenceAndRejectsCapture(t *testing.T) {
	admitted := domain.SecurityState{Generation: 2}
	locked := domain.SecurityState{Generation: 3, Protected: true}
	var epoch atomic.Value
	epoch.Store(admitted)
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) })
	fence, signal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer signal.Close()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Close().Return().Once()
	rendered := make(chan struct{}, 1)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, _ map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
		if s.Security == admitted {
			epoch.Store(locked)
			rendered <- struct{}{}
			return fence, nil
		}
		return nil, nil
	}).Twice()
	scenes := make(chan domain.Scene, 1)
	incoming := make(chan domain.CaptureRequest)
	replies := make(chan domain.CaptureDone, 2)
	proof := make(chan ports.SecurityBackendEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Security: gate, SecurityEvents: proof, Instance: 8, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, incoming)
	}()
	dst := sessionCaptureFile(t)
	incoming <- domain.CaptureRequest{ID: 92, Dst: dst}
	scenes <- domain.Scene{Security: admitted, Seq: 2}
	securityReceive(t, rendered)
	if _, err := fence.Stat(); err != nil {
		t.Fatal("pending renderer read fence closed early")
	}
	select {
	case p := <-proof:
		t.Fatalf("proof on stalled old render %+v", p)
	default:
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	result := securityReceive(t, replies)
	if result.ID != 92 || result.Err == nil {
		t.Fatal(result)
	}
	securityReceive(t, proof)
	cancel()
	if err := securityReceive(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := fence.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("completed render fence leaked")
	}
	if _, err := dst.File.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("rejected capture FD leaked")
	}
	select {
	case extra := <-replies:
		t.Fatalf("duplicate reply %+v", extra)
	default:
	}
}
