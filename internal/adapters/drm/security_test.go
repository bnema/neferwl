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
	"golang.org/x/sys/unix"
)

// A generated mock through the real ports interface, using a coherent atomic
// test state so transitions can race with the output owner without data races.
func securityGate(t *testing.T, o *Output, state *atomic.Uint64) {
	k := portsmocks.NewMockSessionSecurity(t)
	k.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
		v := state.Load()
		return ports.SecurityState{Generation: ports.LockGeneration(v >> 1), Protected: v&1 != 0}
	})
	o.Security = k
	o.Instance = 77
	o.SecurityEvents = make(chan ports.SecurityBackendEvent, 8)
}

func TestSecurityPreparationProofAndFailure(t *testing.T) {
	for _, stage := range []string{"success", "off", "disable", "render", "pending", "changed"} {
		t.Run(stage, func(t *testing.T) {
			var failures []error
			if stage == "disable" {
				failures = []error{unix.EACCES}
			}
			o, k, commits := testOutput(t, failures...)
			var state atomic.Uint64
			state.Store(3)
			securityGate(t, o, &state)
			events := make(chan ports.SecurityBackendEvent, 8)
			o.SecurityEvents = events
			r := portsmocks.NewMockRenderer(t)
			if stage == "pending" {
				f, _ := protectionFence(t, false)
				o.frame.begin(pendingFrame{fences: []*os.File{f}}, time.Now())
			} else if stage != "disable" {
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(ports.Scene{Background: "#000000"}, map[ports.WindowID]ports.SurfaceContent(nil)).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
					if stage == "changed" {
						state.Store(5)
					}
					if stage == "render" {
						return nil, errors.New("render refused")
					}
					return nil, nil
				}).Once()
				if stage != "off" && stage != "render" {
					k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
				}
			}
			o.wantOff = stage == "off"
			err := o.prepareSecurity(context.Background(), r)
			wantErr := stage == "disable" || stage == "render" || stage == "pending"
			if (err != nil) != wantErr {
				t.Fatalf("result %v", err)
			}
			if stage == "success" || stage == "off" {
				select {
				case ev := <-events:
					proof, ok := ev.(ports.SecurityOutputProof)
					kind := ports.ProtectionProtectedFrame
					if stage == "off" {
						kind = ports.ProtectionInactiveOutput
					}
					if !ok || proof.Proof != (ports.OutputProtection{Generation: 1, Instance: 77, Kind: kind}) {
						t.Fatalf("proof %+v", ev)
					}
				default:
					t.Fatal("missing proof")
				}
			} else if len(events) != 0 || o.securityPrepared {
				t.Fatal("failure or changed epoch certified protection")
			}
			if stage == "pending" && len(*commits) != 0 {
				t.Fatal("pending client fence queued blocking KMS")
			}
		})
	}
}

func TestSecurityRejectsStaleAcquireReleaseScenes(t *testing.T) {
	o, _, commits := testOutput(t)
	var state atomic.Uint64
	securityGate(t, o, &state)
	r := portsmocks.NewMockRenderer(t)
	state.Store(3)
	if _, err := o.submitFrame(context.Background(), r, ports.Scene{}, nil, nil, nil, nil); !errors.Is(err, errSecurityScene) {
		t.Fatalf("old desktop admitted: %v", err)
	}
	state.Store(4)
	if _, err := o.submitFrame(context.Background(), r, ports.Scene{Security: ports.SecurityState{Generation: 1, Protected: true}}, nil, nil, nil, nil); !errors.Is(err, errSecurityScene) {
		t.Fatalf("old lock admitted on release: %v", err)
	}
	if len(*commits) != 0 {
		t.Fatal("stale epoch committed")
	}
}

func TestSecurityProtectedCompositionDetachesCursorAndOverlay(t *testing.T) {
	o, _, commits := testOutput(t)
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	o.observeSecurity()
	o.securityPrepared = true
	o.cursor.image = true
	o.cursor.Move(8, 8)
	o.overlay = &plane{id: tOverlay, props: planeProps}
	s, c := fullscreenScene()
	s.Security = ports.SecurityState{Generation: 1, Protected: true}
	s.CaptureScene = &ports.Scene{}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(scene ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if scene.CaptureScene != nil {
			t.Fatal("hidden capture crossed protected path")
		}
		return nil, nil
	}).Once()
	direct, err := o.submitFrame(context.Background(), r, s, c, nil, nil, nil)
	if err != nil || direct {
		t.Fatalf("direct=%v err=%v", direct, err)
	}
	last := (*commits)[0]
	if last.flags&flipAsyncFlag != 0 {
		t.Fatal("protected tearing")
	}
	for _, plane := range []uint32{tCursor, tOverlay} {
		if fb, _ := last.req.value(plane, pFB); fb != 0 {
			t.Fatalf("desktop plane %d attached", plane)
		}
	}
}

func TestSecurityCloseReadsGateAfterOwnerStopped(t *testing.T) {
	o, k, commits := testOutput(t)
	var state atomic.Uint64
	securityGate(t, o, &state)
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	state.Store(3) // no wake or owner loop observes it
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor)
}

func TestSecurityCommitStateReadsGateBeforeCursorCommit(t *testing.T) {
	o, _, commits := testOutput(t)
	var state atomic.Uint64
	securityGate(t, o, &state)
	o.cursor.image = true
	o.cursor.Move(10, 10)
	state.Store(3)
	if err := o.commitState(true); !errors.Is(err, errSecurityScene) {
		t.Fatalf("cursor result: %v", err)
	}
	if len(*commits) != 0 || !o.protected {
		t.Fatal("cursor committed before owner observed lock")
	}
}
