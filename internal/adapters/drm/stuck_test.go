package drm

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
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
	h := startStuck(t, nil, errs...)
	h.scenes <- ports.Scene{Seq: 1}
	waitFor(t, func() bool { return h.count() >= n })
	return h.stop()
}

// stuckRun is an output run by startStuck.
type stuckRun struct {
	t         *testing.T
	scenes    chan ports.Scene
	contents  chan ports.SurfaceContent
	active    chan bool
	presented chan ports.OutputPresented
	count     func() int
	stop      func() []commitRec
}

// startStuck runs an output whose commits return errs (after the start
// modeset test and modeset) and whose CRTC never sends an event. render
// makes each frame's fence once the output started (nil: none).
func startStuck(t *testing.T, render func() *os.File, errs ...error) *stuckRun {
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
	var started atomic.Bool
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if render == nil || !started.Load() {
			return nil, nil
		}
		return render(), nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	h := &stuckRun{t: t, scenes: make(chan ports.Scene, 1), contents: make(chan ports.SurfaceContent, 1), active: make(chan bool, 1), presented: make(chan ports.OutputPresented, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, h.active, h.scenes, h.contents, nil, h.presented)
	}()
	h.count = func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	h.stop = func() []commitRec {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		commitMu.Lock()
		defer commitMu.Unlock()
		return append([]commitRec(nil), (*commits)...)
	}
	waitFor(t, func() bool { return h.count() == 2 })
	started.Store(true)
	return h
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

// fencePipe is a fence that signals when its write end is written.
func fencePipe(t *testing.T) (fence, signal *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

// A frame whose GPU fence has not signalled is slow, not lost: it keeps
// waiting past the limit; once the fence signalled and still no event
// came, the output modesets.
func TestUnsignalledFenceDelaysRecovery(t *testing.T) {
	var mu sync.Mutex
	var signals []*os.File
	h := startStuck(t, func() *os.File {
		f, w := fencePipe(t)
		mu.Lock()
		signals = append(signals, w)
		mu.Unlock()
		return f
	})
	h.scenes <- ports.Scene{Seq: 1}
	waitFor(t, func() bool { return h.count() == 3 })
	time.Sleep(3 * 200 * time.Millisecond)
	if n := h.count(); n != 3 {
		t.Fatalf("%d commits while the frame's fence is unsignalled", n)
	}
	mu.Lock()
	_, _ = signals[0].Write([]byte{1})
	mu.Unlock()
	waitFor(t, func() bool { return h.count() >= 4 })
	if c := h.stop(); c[3].flags&atomicAllowModes == 0 {
		t.Fatalf("no modeset once the fence signalled: %#x", c[3].flags)
	}
}

// A frame refused with EBUSY may still be read by the GPU: what was seen
// is reported only once its fence signalled.
func TestSeenWaitsForRefusedFrameFence(t *testing.T) {
	var mu sync.Mutex
	var signals []*os.File
	h := startStuck(t, func() *os.File {
		f, w := fencePipe(t)
		mu.Lock()
		signals = append(signals, w)
		mu.Unlock()
		return f
	}, unix.EBUSY, unix.EACCES)
	h.scenes <- ports.Scene{Seq: 1}
	// EBUSY, then the retry loses DRM master: nothing is pending.
	waitFor(t, func() bool { return h.count() == 4 })
	h.contents <- ports.SurfaceContent{ID: 1, Seq: 7}
	select {
	case p := <-h.presented:
		t.Fatalf("seen reported while refused frames may be read: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	for _, w := range signals {
		_, _ = w.Write([]byte{1})
	}
	mu.Unlock()
	select {
	case p := <-h.presented:
		if p.Seen[1] != 7 {
			t.Fatalf("report %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seen not reported once the fences signalled")
	}
	h.stop()
}

// An EBUSY run ends when commits stop being refused: a later EBUSY is
// retried, not taken as the same run lasting past the limit.
func TestLaterBusyStartsNewRun(t *testing.T) {
	o, _, _ := testOutput(t, unix.EBUSY, unix.EBUSY)
	o.stuckAfter = 100 * time.Millisecond
	enabled := true
	o.commitFailed(o.commitFrame(70, nil, false, false, pendingFrame{}), &enabled)
	time.Sleep(busyRetry)
	if o.expire() {
		t.Fatal("first EBUSY retry modesets")
	}
	time.Sleep(2 * o.stuckAfter)
	o.commitFailed(o.commitFrame(70, nil, false, false, pendingFrame{}), &enabled)
	time.Sleep(busyRetry)
	if o.expire() {
		t.Fatal("a new EBUSY taken as the old run: modeset")
	}
}

// A modeset that fails keeps what is on screen: buffers still shown are
// not released under the plane.
func TestFailedModesetKeepsShown(t *testing.T) {
	o, k, _ := testOutput(t, unix.EACCES)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	o.shown, o.queued = 5, 6
	if err := o.modeset(); err == nil {
		t.Fatal("modeset succeeded")
	}
	if o.shown != 5 || o.queued != 6 {
		t.Fatalf("shown %d queued %d after a failed modeset", o.shown, o.queued)
	}
}
