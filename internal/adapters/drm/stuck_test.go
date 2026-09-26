package drm

import (
	"context"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/nefertty/internal/mocks/ports"
	"github.com/bnema/nefertty/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// runStuck runs an output whose commits return errs (after the start
// modeset test and modeset) and whose CRTC never sends an event. It
// returns the recorded commits once n were made, failing after 2s.
func runStuck(t *testing.T, n int, errs ...error) []commitRec {
	t.Helper()
	o, k, commits, commitMu := testOutputMu(t, append([]error{nil, nil}, errs...)...)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	o.stuckAfter = 200 * time.Millisecond
	o.flipped = make(chan flipEvent) // no event ever
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	scenes := make(chan ports.Scene, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, nil, nil, make(chan ports.OutputPresented, 64))
	}()
	count := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	waitFor(t, func() bool { return count() == 2 })
	scenes <- ports.Scene{Seq: 1}
	waitFor(t, func() bool { return count() >= n })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	commitMu.Lock()
	defer commitMu.Unlock()
	return append([]commitRec(nil), (*commits)...)
}

// A commit whose event never comes must not stop the output: it is
// abandoned with a modeset, then the scene is committed again.
func TestMissingEventRecoversWithModeset(t *testing.T) {
	c := runStuck(t, 5)
	if c[2].user&3 != userFrame {
		t.Fatalf("commit 2 is not a frame: %+v", c[2])
	}
	if c[3].flags&atomicAllowModes == 0 {
		t.Fatalf("no recovery modeset after the missing event: flags %#x", c[3].flags)
	}
	if c[4].user&3 != userFrame || c[4].user>>userKindBits <= c[2].user>>userKindBits {
		t.Fatalf("scene not committed again after the modeset: %+v", c[4])
	}
}

// EBUSY with no commit of ours in flight gets no event: the commit is
// retried without one.
func TestBusyWithoutEventRetries(t *testing.T) {
	c := runStuck(t, 4, unix.EBUSY)
	if c[2].user&3 != userFrame || c[3].user&3 != userFrame || c[3].flags&atomicAllowModes != 0 {
		t.Fatalf("busy frame not retried: %+v", c[2:])
	}
}

// EBUSY that lasts past the limit ends in a modeset, and a modeset
// refused as busy is retried too.
func TestLastingBusyModesets(t *testing.T) {
	errs := make([]error, 8)
	for i := range errs {
		errs[i] = unix.EBUSY
	}
	c := runStuck(t, 2+len(errs)+1, errs...)
	found := false
	for _, r := range c[2:] {
		if r.flags&atomicAllowModes != 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("no modeset while EBUSY lasted")
	}
}
