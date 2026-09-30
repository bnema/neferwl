package capture

import (
	"context"
	"image"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// fencePipe is a sync file stand-in from the kernel: readable once written.
// fencePipe comes from pipeline_fence_test.go.

func signalFence(t *testing.T, w *os.File) {
	t.Helper()
	_, err := w.Write([]byte{1})
	require.NoError(t, err)
}

func fenceClosed(f *os.File) bool {
	_, err := f.Stat()
	return err != nil
}

// hiddenPipeline returns a pipeline whose child renders return the given
// fences in order, and the scene/surfaces to submit.
func hiddenPipeline(t *testing.T, fences ...*os.File) (*Pipeline, chan ports.CaptureDone, *portsmocks.MockRenderer) {
	t.Helper()
	replies := make(chan ports.CaptureDone, 8)
	p := NewPipeline(context.Background(), replies)
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	child.EXPECT().EndCapture(frame).Return().Maybe()
	child.EXPECT().BeginCapture().Return(frame, nil).Maybe()
	child.EXPECT().Close().Return().Maybe()
	for _, f := range fences {
		child.EXPECT().Render(mock.Anything, mock.Anything).Return(f, nil).Once()
	}
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	return p, replies, child
}

func hiddenSceneWindows(ids ...ports.WindowID) ports.Scene {
	s := hiddenScene()
	s.CaptureScene.Windows, s.CaptureScene.Layers = nil, nil
	for _, id := range ids {
		s.CaptureScene.Windows = append(s.CaptureScene.Windows, ports.SceneWindow{ID: id})
	}
	return s
}

func submitHiddenOne(t *testing.T, p *Pipeline, s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, id uint64) {
	t.Helper()
	p.SubmitHidden(s, surfaces, []ports.CaptureRequest{hiddenRequest(t, id, image.Rect(0, 0, 200, 100))})
}

// GO001: a failing or cancelled wait still closes every fence, the ones not
// waited for included.
func TestWaitHiddenClosesEveryFenceOnCancel(t *testing.T) {
	f1, _ := fencePipe(t)
	f2, _ := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1, f2)
	defer p.Close(nil)
	s := hiddenSceneWindows(5)
	submitHiddenOne(t, p, s, nil, 1)
	submitHiddenOne(t, p, s, nil, 2)
	require.Equal(t, 2, p.off.nf)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, p.WaitHidden(ctx))
	require.Zero(t, p.off.nf)
	require.True(t, fenceClosed(f1), "first fence closed")
	require.True(t, fenceClosed(f2), "second fence, never waited for, closed too")
	for range 2 {
		awaitCapture(t, replies)
	}
}

// A terminal fence error must cancel and join readback before closing the
// device. Holds remain in place until Close has made GPU reads impossible.
func TestTerminalChildFenceClosesPipelineBeforeClearingHolds(t *testing.T) {
	read, failed, err := os.Pipe()
	require.NoError(t, err)
	defer failed.Close()
	pending, _ := fencePipe(t)
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 2))
	defer p.Close(nil)
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(pending).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(failed, nil).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	ended := false
	child.EXPECT().EndCapture(frame).Run(func(ports.CaptureFrame) { ended = true }).Once()
	child.EXPECT().Close().Run(func() {
		require.True(t, ended, "worker joined and lease returned before device closes")
		require.Equal(t, 1, p.off.nf, "hold retained until device closes")
	}).Once()
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	submitHiddenOne(t, p, hiddenSceneWindows(5), map[ports.WindowID]ports.SurfaceContent{5: {Seq: 2}}, 1)
	_, reads, busy := p.CapHiddenSeen(map[ports.WindowID]uint64{5: 7})
	require.True(t, busy)
	require.Equal(t, map[ports.WindowID]uint64{5: 2}, reads)
	require.NoError(t, read.Close()) // write end now polls POLLERR
	p.Retire(ports.Scene{})          // session end must not wait forever on the error
	require.Nil(t, p.off.r, "failed device closed")
	require.Nil(t, p.HiddenCompleted(), "failed worker joined")
	seen := map[ports.WindowID]uint64{5: 7}
	got, reads, busy := p.CapHiddenSeen(seen)
	require.False(t, busy)
	require.Empty(t, reads, "closed device lifts non-expiring holds")
	require.Equal(t, seen, got)
	require.True(t, fenceClosed(failed))
}

func TestWaitHiddenWaitsForSignalledFences(t *testing.T) {
	f1, w1 := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1)
	defer p.Close(nil)
	submitHiddenOne(t, p, hiddenSceneWindows(5), nil, 1)
	signalFence(t, w1)
	require.NoError(t, p.WaitHidden(context.Background()))
	require.True(t, fenceClosed(f1))
	awaitCapture(t, replies)
}

// GO002: the ceiling of a fence covers only the windows its render drew, per
// fence, and a signalled fence lifts it.
func TestCapHiddenSeenPerFenceCeilings(t *testing.T) {
	f1, w1 := fencePipe(t)
	f2, w2 := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1, f2)
	defer p.Close(nil)

	// Render 1 drew window 5 (content Seq 2); render 2 drew 5 (Seq 6) and 6 (Seq 3).
	submitHiddenOne(t, p, hiddenSceneWindows(5), map[ports.WindowID]ports.SurfaceContent{5: {ID: 5, Seq: 2}}, 1)
	submitHiddenOne(t, p, hiddenSceneWindows(5, 6), map[ports.WindowID]ports.SurfaceContent{5: {ID: 5, Seq: 6}, 6: {ID: 6, Seq: 3}}, 2)

	// 5 is capped by the lowest ceiling among the unfinished renders that drew
	// it; 6 by the second; the display's window 1 and an unrelated 9 are free.
	seen := map[ports.WindowID]uint64{1: 40, 5: 8, 6: 8, 9: 50}
	got, reads, capped := p.CapHiddenSeen(seen)
	require.Equal(t, map[ports.WindowID]uint64{5: 2, 6: 3}, reads)
	require.True(t, capped)
	require.Equal(t, map[ports.WindowID]uint64{1: 40, 5: 2, 6: 3, 9: 50}, got)
	require.Equal(t, uint64(8), seen[5], "input untouched")

	// Render 1 finishes: window 5 is now limited by render 2 only.
	signalFence(t, w1)
	got, reads, capped = p.CapHiddenSeen(seen)
	require.Equal(t, map[ports.WindowID]uint64{5: 6, 6: 3}, reads)
	require.True(t, capped)
	require.Equal(t, map[ports.WindowID]uint64{1: 40, 5: 6, 6: 3, 9: 50}, got)

	// A window the earlier render never saw is not capped by it: a display
	// window that is new to the child releases at once.
	signalFence(t, w2)
	got, reads, capped = p.CapHiddenSeen(seen)
	require.Empty(t, reads)
	require.False(t, capped)
	require.Equal(t, seen, got)
	for range 2 {
		awaitCapture(t, replies)
	}
}

// A window the child drew with no content yet is capped at 0; one it never
// drew is not capped at all.
func TestCapHiddenSeenMissingContentCapsToZero(t *testing.T) {
	f1, _ := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1)
	defer p.Close(nil)
	submitHiddenOne(t, p, hiddenSceneWindows(5), map[ports.WindowID]ports.SurfaceContent{}, 1)
	got, reads, capped := p.CapHiddenSeen(map[ports.WindowID]uint64{5: 4, 7: 4})
	require.Equal(t, map[ports.WindowID]uint64{5: 0}, reads)
	require.True(t, capped)
	require.Equal(t, map[ports.WindowID]uint64{5: 0, 7: 4}, got)
	awaitCapture(t, replies)
}

func TestCapHiddenSeenNoAllocationInSteadyState(t *testing.T) {
	f1, _ := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1)
	defer p.Close(nil)
	submitHiddenOne(t, p, hiddenSceneWindows(5), map[ports.WindowID]ports.SurfaceContent{5: {ID: 5, Seq: 2}}, 1)
	seen := map[ports.WindowID]uint64{1: 40, 5: 8}
	p.CapHiddenSeen(seen) // warm the scratch map
	allocs := testing.AllocsPerRun(100, func() { p.CapHiddenSeen(seen) })
	require.LessOrEqual(t, allocs, float64(1), "only the poll descriptor may allocate")
	t.Logf("outstanding child report: %.1f allocs", allocs)
	awaitCapture(t, replies)
	var none *Pipeline
	got, reads, capped := none.CapHiddenSeen(seen)
	require.Nil(t, reads)
	require.False(t, capped)
	require.Equal(t, seen, got)
}

// GO003: a third render is refused before the renderer is asked to draw it.
func TestSubmitHiddenThirdRenderRefusedBeforeRender(t *testing.T) {
	f1, _ := fencePipe(t)
	f2, _ := fencePipe(t)
	p, replies, _ := hiddenPipeline(t, f1, f2) // only two Render expectations
	defer p.Close(nil)
	s := hiddenSceneWindows(5)
	submitHiddenOne(t, p, s, nil, 1)
	submitHiddenOne(t, p, s, nil, 2)
	submitHiddenOne(t, p, s, nil, 3)
	byID := map[uint64]error{}
	for range 3 {
		d := awaitCapture(t, replies)
		byID[d.ID] = d.Err
	}
	require.NoError(t, byID[1])
	require.NoError(t, byID[2])
	require.ErrorIs(t, byID[3], ErrOffscreenBusy)
}

// GO005: an idle child of an ended session is closed once, whether its lease
// or its fence came back last.
func TestRetireClosesIdleChildOnce(t *testing.T) {
	f1, w1 := fencePipe(t)
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(f1, nil).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	child.EXPECT().EndCapture(frame).Return().Once()
	child.EXPECT().Close().Return().Once() // exactly once
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })

	submitHiddenOne(t, p, hiddenSceneWindows(5), nil, 1)
	awaitCapture(t, replies)
	p.RecycleHidden(<-p.HiddenCompleted())

	// The fence is still pending: the child stays.
	p.Retire(ports.Scene{})
	require.NotNil(t, p.off.r)
	signalFence(t, w1)
	p.Retire(ports.Scene{})
	require.Nil(t, p.off.r, "closed once idle")
	require.Nil(t, p.HiddenCompleted(), "no child, no channel")
	p.Retire(ports.Scene{}) // nothing left to close
	p.Close(nil)
}

// GO006: a readable fence (or an invalid descriptor) is done; POLLERR and poll
// failures are terminal; timeouts and interrupts stay pending.
func TestPollResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		err     error
		revents int16
		want    fenceState
	}{
		{"readable", 1, nil, unix.POLLIN, fenceDone},
		{"timeout", 0, nil, 0, fencePending},
		{"interrupted", -1, unix.EINTR, 0, fencePending},
		{"other poll error", -1, unix.EIO, 0, fenceFailed},
		{"invalid poll descriptor", -1, unix.EBADF, 0, fenceDone},
		{"invalid descriptor event", 1, nil, unix.POLLNVAL, fenceDone},
		{"gpu error status", 1, nil, unix.POLLERR, fenceFailed},
		{"error with data", 1, nil, unix.POLLIN | unix.POLLERR, fenceFailed},
		{"hangup only", 1, nil, unix.POLLHUP, fencePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pollResult(tc.n, tc.err, tc.revents))
		})
	}
}

func TestPollFenceOnRealDescriptors(t *testing.T) {
	r, w := fencePipe(t)
	require.Equal(t, fencePending, pollFence(r), "nothing written yet")
	signalFence(t, w)
	require.Equal(t, fenceDone, pollFence(r))
	closed, _ := fencePipe(t)
	require.NoError(t, closed.Close())
	require.Equal(t, fenceDone, pollFence(closed), "a closed descriptor has nothing to wait for")
	allocs := testing.AllocsPerRun(100, func() { pollFence(r) })
	require.Zero(t, allocs, "polling a fence does not allocate")
	t.Logf("pollFence: %.1f allocs", allocs)
}
