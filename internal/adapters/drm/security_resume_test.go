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
	"golang.org/x/sys/unix"
)

func TestSecurityUnlockedResumeUsesOwnerRetry(t *testing.T) {
	for _, stage := range []string{"engage-during-request", "master-loss"} {
		t.Run(stage, func(t *testing.T) {
			var failures []error
			if stage == "master-loss" {
				failures = []error{unix.EACCES}
			}
			o, k, commits, mu := testOutputMu(t, failures...)
			o.cursor, o.tearing = nil, false
			var state atomic.Uint64
			securityGate(t, o, &state)
			events := make(chan ports.SecurityBackendEvent, 4)
			o.SecurityEvents = events
			active := make(chan bool, 2)
			active <- false // retain existing images; first modeset is resume
			wake := make(chan ports.SecurityState, 1)
			o.SecurityChanges = wake
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
				if s.Seq != 0 || s.Background != "#000000" || len(s.Windows) != 0 {
					t.Fatal("resume rendered desktop")
				}
				return nil, nil
			}).Once()
			r.EXPECT().Close().Return().Once()
			var blobs atomic.Int32
			k.EXPECT().createBlob(mock.Anything).RunAndReturn(func([]byte) (uint32, error) {
				if blobs.Add(1) == 1 && stage == "engage-during-request" {
					state.Store(3)
				}
				return 99, nil
			})
			k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, nil, nil, nil)
			}()
			active <- true
			if stage == "master-loss" {
				waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(*commits) == 1 })
				state.Store(3)
				wake <- ports.SecurityState{}
				select {
				case ev := <-events:
					if _, ok := ev.(ports.SecurityOutputProof); ok {
						t.Fatal("master loss forged proof")
					}
				case <-time.After(20 * time.Millisecond):
				}
				select {
				case err := <-done:
					t.Fatalf("resume stopped owner: %v", err)
				default:
				}
				active <- true // only real seat reacquisition permits preparation
			}
			for {
				select {
				case ev := <-events:
					if p, ok := ev.(ports.SecurityOutputProof); ok {
						if p.Proof.Generation != 1 || p.Proof.Instance != 77 {
							t.Fatalf("proof %+v", p)
						}
						cancel()
						if err := <-done; err != nil {
							t.Fatal(err)
						}
						return
					}
				case err := <-done:
					t.Fatalf("resume ended before fresh proof: %v", err)
				case <-ctx.Done():
					t.Fatal("missing fresh resume proof")
				}
			}
		})
	}
}
