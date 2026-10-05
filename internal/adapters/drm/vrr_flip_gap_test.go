package drm

import (
	"context"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// The gap starts only after a game frame's flip under VRR.
func TestStartFlipGap(t *testing.T) {
	for _, tc := range []struct {
		name             string
		gap              time.Duration
		frame, vrr, game bool
		want             bool
	}{
		{"game frame under VRR", time.Millisecond, true, true, true, true},
		{"gap off", 0, true, true, true, false},
		{"state commit keeps the gap", time.Millisecond, false, true, true, true},
		{"state commit keeps no gap", time.Millisecond, false, false, false, false},
		{"VRR off", time.Millisecond, true, false, true, false},
		{"desktop under VRR hold", time.Millisecond, true, true, false, false},
	} {
		// A gap is pending from the last game frame when it applies.
		pending := time.Time{}
		if tc.vrr && tc.game {
			pending = time.Now().Add(time.Hour)
		}
		o := &Output{vrrFlipGap: tc.gap, vrrOn: tc.vrr, vrrGame: tc.game, flipGapUntil: pending}
		o.startFlipGap(pendingFrame{frame: tc.frame}, monotonic(), time.Now())
		if got := !o.flipGapUntil.IsZero(); got != tc.want {
			t.Errorf("%s: gap started %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A cursor commit that completes inside the gap does not end it.
func TestStateCommitKeepsFlipGap(t *testing.T) {
	o, _, commits := testOutput(t)
	o.vrrFlipGap, o.vrrOn, o.vrrGame = time.Hour, true, true
	seen := map[ports.WindowID]uint64{}
	if err := o.commitFrame(70, nil, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	o.completed(eventOf((*commits)[0]), seen)
	gap := o.flipGapUntil
	if gap.IsZero() {
		t.Fatal("no gap after a game frame")
	}
	o.cursor.image = true
	o.lastFrame = time.Time{} // the cursor commits alone
	o.cursor.Move(3, 3)
	if err := o.commitState(o.stateVRR()); err != nil || len(*commits) != 2 {
		t.Fatalf("cursor commit: %v, %d commits", err, len(*commits))
	}
	o.completed(eventOf((*commits)[1]), seen)
	if o.flipGapUntil != gap {
		t.Fatalf("gap %v after a cursor commit, want %v", o.flipGapUntil, gap)
	}
}

// A modeset drops a pending gap: it belonged to the old display state.
func TestModesetClearsFlipGap(t *testing.T) {
	o, k, _ := testOutput(t)
	o.flipGapUntil = time.Now().Add(time.Hour)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if !o.flipGapUntil.IsZero() {
		t.Fatalf("gap until %v after modeset", o.flipGapUntil)
	}
}

// Run commits the next game frame no sooner than the gap after the flip
// event, then commits it on its own when the gap ends.
func TestRunWaitsVRRFlipGap(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	o.vrrFlipGap = 30 * time.Millisecond
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	pipeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	frames := func() (commitRec, int) {
		commitMu.Lock()
		defer commitMu.Unlock()
		n, last := 0, commitRec{}
		for _, c := range *commits {
			if c.user&3 == userFrame {
				n, last = n+1, c
			}
		}
		return last, n
	}
	scenes := make(chan ports.Scene, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	// A composed fullscreen window: a game frame under VRR.
	game := ports.Scene{OutputWidth: 200, OutputHeight: 100,
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}},
		Layers:  []ports.SceneLayer{{ID: 5, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 10, H: 10}}}}
	scenes <- game
	waitFor(t, func() bool { _, n := frames(); return n == 1 })
	last, _ := frames()
	// The next frame is already waiting when the flip arrives.
	game.Seq = 1
	scenes <- game
	flips <- flipEvent{crtc: tCrtc, user: last.user, when: time.Second, seq: 1}
	// Run handles the flip after it is sent: its commit is at least the
	// gap after this point, whatever the scheduling delay.
	sent := time.Now()
	waitFor(t, func() bool { _, n := frames(); return n == 2 })
	commitMu.Lock()
	second := (*commits)[len(*commits)-1].at
	commitMu.Unlock()
	if waited := second.Sub(sent); waited < o.vrrFlipGap-time.Millisecond {
		t.Fatalf("next frame committed %s after the flip, gap %s", waited, o.vrrFlipGap)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
