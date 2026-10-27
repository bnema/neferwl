package drm

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestSecurityUnlockedResumeUsesOwnerRetry(t *testing.T) {
	for _, stage := range []string{"engage-during-request", "master-loss"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var state atomic.Uint64
				var rule func(*atomicReq, uint32) error
				if stage == "master-loss" {
					// The lock engages as the kernel refuses the first enable for
					// lack of master: the retry must be a protected modeset.
					refused := false
					rule = func(*atomicReq, uint32) error {
						if refused {
							return nil
						}
						refused = true
						state.Store(3)
						return unix.EACCES
					}
				}
				o, k, commits, mu := testOutputRules(t, rule)
				o.cursor, o.tearing = nil, false
				securityGate(t, o, &state)
				events := make(chan ports.SecurityBackendEvent, 4)
				o.SecurityEvents = events
				active := make(chan bool, 2)
				active <- false // retain existing images; first modeset is resume
				wake := make(chan ports.SecurityState, 1)
				o.SecurityChanges = wake
				r := portsmocks.NewMockRenderer(t)
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) (*os.File, error) {
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
				setOutputRenderer(t, o, r)
				go func() {
					done <- o.Run(ctx, nil, active, nil, nil, nil, nil, nil, nil, nil)
				}()
				active <- true
				if stage == "master-loss" {
					waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(*commits) >= 1 })
					wake <- ports.SecurityState{}
					// The seat reported active before master came back: the owner
					// retries the protected modeset itself, with no second seat
					// event. The proof below must follow that successful retry.
				}
				for {
					select {
					case ev := <-events:
						if p, ok := ev.(ports.SecurityOutputProof); ok {
							if p.Proof.Generation != 1 || p.Proof.Instance != 77 {
								t.Fatalf("proof %+v", p)
							}
							if stage == "master-loss" {
								mu.Lock()
								n := len(*commits)
								mu.Unlock()
								if n < 2 {
									t.Fatal("master loss forged proof: no successful retry")
								}
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
		})
	}
}
