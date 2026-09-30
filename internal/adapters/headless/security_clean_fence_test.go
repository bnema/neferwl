package headless

import (
	"context"
	"errors"
	"image"
	"os"
	"sync/atomic"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestCleanFenceRetainsReadsAcrossEngageAndOff(t *testing.T) {
	for _, finish := range []string{"signal", "cancel", "render-error"} {
		t.Run(finish, func(t *testing.T) {
			admitted := ports.SecurityState{Generation: 2}
			protected := ports.SecurityState{Generation: 3, Protected: true}
			var epoch atomic.Value
			epoch.Store(admitted)
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
			fence, signal, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer signal.Close()
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().Close().Return().Once()
			rendered := make(chan struct{}, 1)
			releaseRender := make(chan struct{})
			failure := errors.New("clean render failed after submission")
			r.EXPECT().Render(mock.MatchedBy(func(s ports.Scene) bool { return s.Security == admitted && s.Seq == 0 }), mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
				rendered <- struct{}{}
				<-releaseRender
				if finish == "render-error" {
					return fence, failure
				}
				return fence, nil
			}).Once()
			if finish == "signal" {
				r.EXPECT().Render(mock.MatchedBy(func(s ports.Scene) bool { return s.Security == protected && s.Seq == 0 }), mock.Anything).Return(nil, nil).Once()
			}
			// Native capture and Pixels have no expectations; neither is admitted.
			scenes := make(chan ports.Scene, 2)
			contents := make(chan ports.SurfaceContent, 1)
			incoming := make(chan ports.CaptureRequest)
			presented := make(chan ports.OutputPresented, 8)
			proofs := make(chan ports.SecurityBackendEvent, 2)
			replies := make(chan ports.CaptureDone, 2)
			changes := make(chan ports.SecurityState, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{Security: gate, SecurityChanges: changes, SecurityEvents: proofs, Instance: 7, Width: 2, Height: 2, Presented: presented, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents, nil, incoming)
			}()
			dst := sessionCaptureFile(t)
			incoming <- ports.CaptureRequest{ID: 99, Clean: true, Session: 7, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: dst}
			contents <- ports.SurfaceContent{ID: 1, Seq: 8, SHM: &ports.SHMBuffer{Pool: 1}}
			scene := sessionScene()
			scene.Security = admitted
			scene.Scale = 1
			scene.OutputWidth = 2
			scene.OutputHeight = 2
			scenes <- scene
			securityReceive(t, rendered)
			// Prior non-render reports are outside this read lifetime; establish the
			// cut at clean GPU submission before exercising transition/off handling.
			for len(presented) > 0 {
				<-presented
			}
			epoch.Store(protected)
			scenes <- ports.Scene{Security: protected, Off: true}
			changes <- protected
			close(releaseRender)
			// A bounded observation interval catches the original immediate fence
			// close/report path without assuming a polling schedule.
			select {
			case report := <-presented:
				t.Fatalf("GPU reads reported before clean fence: %+v", report)
			case <-time.After(150 * time.Millisecond):
			}
			if _, err := fence.Stat(); err != nil {
				t.Fatalf("unsignalled clean read fence closed: %v", err)
			}
			select {
			case proof := <-proofs:
				t.Fatalf("protection path bypassed pending clean reads: %+v", proof)
			default:
			}
			if finish == "cancel" {
				cancel()
			} else {
				if _, err := signal.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
			}
			if finish == "signal" {
				securityReceive(t, proofs)
				cancel()
			}
			runErr := securityReceive(t, done)
			if finish == "render-error" {
				if !errors.Is(runErr, failure) {
					t.Fatal(runErr)
				}
			} else if runErr != nil {
				t.Fatal(runErr)
			}
			if _, err := fence.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("clean fence leaked: %v", err)
			}
			reply := securityReceive(t, replies)
			if reply.ID != 99 || reply.Err == nil {
				t.Fatal(reply)
			}
			if _, err := dst.File.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("capture destination leaked")
			}
			select {
			case extra := <-replies:
				t.Fatalf("duplicate capture reply %+v", extra)
			default:
			}
			if finish != "signal" {
				select {
				case proof := <-proofs:
					t.Fatalf("cancel/error falsely proved protection: %+v", proof)
				default:
				}
				select {
				case report := <-presented:
					t.Fatalf("cancel/error falsely reported reads: %+v", report)
				default:
				}
			}
		})
	}
}
