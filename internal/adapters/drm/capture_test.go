package drm

import (
	"context"
	"errors"
	"image"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// sessionRequest is a capture request; exclude sets the exclusion of session 7.
func sessionRequest(t *testing.T, id uint64, exclude bool) ports.CaptureRequest {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "capture")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, f.Truncate(16))
	q := ports.CaptureRequest{ID: id, Dst: ports.SHMBuffer{File: f}, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1}
	if exclude {
		q.Exclude, q.Session, q.CaptureRevision = true, 7, 1
	}
	return q
}

func drmSessionScene() ports.Scene {
	return ports.Scene{Seq: 4, Windows: []ports.SceneWindow{{ID: 1}}, Layers: []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop}},
		Capture: &ports.SceneCapture{Session: 7, Revision: 1, Excluded: []ports.WindowID{10}}}
}

// The excluded frame is drawn and copied before the displayed frame, on the
// same target; the standard capture is taken after the displayed frame.
func TestSubmitFrameExcludeBeforeDisplayedThenStandard(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	var order []string
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if s.Capture == nil {
			require.Zero(t, s.Seq)
			require.Len(t, s.Layers, 0, "excluded HUD layer left out of the excluded frame")
			order = append(order, "excluded")
		} else {
			require.Len(t, s.Layers, 1, "displayed frame keeps the HUD")
			order = append(order, "shown")
		}
		return nil, nil
	}).Twice()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().RunAndReturn(func() (ports.CaptureFrame, error) {
		order = append(order, "copy")
		return frame, nil
	}).Twice()
	r.EXPECT().EndCapture(frame).Return().Twice()
	replies := make(chan ports.CaptureDone, 4)
	pipeline := capture.NewPipeline(context.Background(), replies)
	requests := []ports.CaptureRequest{sessionRequest(t, 1, true), sessionRequest(t, 2, false)}
	direct, err := o.submitFrame(context.Background(), r, drmSessionScene(), nil, nil, requests, pipeline)
	require.NoError(t, err)
	require.False(t, direct)
	require.Equal(t, []string{"excluded", "copy", "shown", "copy"}, order)
	for _, q := range requests {
		require.True(t, capture.Handed(q), "requests handed to the worker")
	}
	pipeline.Close(r)
	require.Len(t, replies, 2)
}

// A fullscreen window would be scanned out: a capture request of the
// displayed frame forces composition.
func TestCaptureRequestPreventsDirectScanout(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	direct, err := o.submitFrame(context.Background(), r, drmSessionScene(), nil, nil, []ports.CaptureRequest{sessionRequest(t, 2, false)}, pipeline)
	require.NoError(t, err)
	require.False(t, direct)
	require.Equal(t, "capture", o.reason)
	pipeline.Close(r)
}

// A full render failing after the Exclude group was handed off: the caller
// still owns only the standard request; the Exclude one is zeroed, so it is
// never answered twice.
func TestSubmitFrameRenderErrorKeepsOnlyUnhandedRequests(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	boom := errors.New("boom")
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, boom).Once()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 4)
	pipeline := capture.NewPipeline(context.Background(), replies)
	requests := []ports.CaptureRequest{sessionRequest(t, 1, true), sessionRequest(t, 2, false)}
	_, err := o.submitFrame(context.Background(), r, drmSessionScene(), nil, nil, requests, pipeline)
	var fatal renderError
	require.ErrorAs(t, err, &fatal)
	owned := 0
	for _, q := range requests {
		if !capture.Handed(q) {
			owned++
			require.Equal(t, uint64(2), q.ID)
		}
	}
	require.Equal(t, 1, owned)
	pipeline.Close(r)
	require.Len(t, replies, 1) // the Exclude capture, answered once by the worker
}

// An Exclude request of a session the scene no longer carries fails closed
// and is never captured from the displayed frame.
func TestSubmitFrameExcludeRequestWithoutSessionFails(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	defer pipeline.Close(r)
	requests := []ports.CaptureRequest{sessionRequest(t, 1, true)}
	scene := drmSessionScene()
	scene.Capture = nil
	_, err := o.submitFrame(context.Background(), r, scene, nil, nil, requests, pipeline)
	require.NoError(t, err)
	got := <-replies
	require.ErrorIs(t, got.Err, capture.ErrSessionInactive)
	require.True(t, capture.Handed(requests[0]))
}

// Capture state alone (a session, an exclusion, a hidden workspace) must
// not cost a fullscreen window its direct scanout: only requests that read
// the displayed frame do.
func TestCaptureStateKeepsDirectScanout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capture *ports.SceneCapture
	}{
		{"no session", nil},
		{"session", &ports.SceneCapture{Shown: 3}},
		{"exclusion", &ports.SceneCapture{Session: 7, Revision: 1, Excluded: []ports.WindowID{10}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, k, _ := testOutput(t)
			o.cursor, o.tearing, o.vrrProp = nil, false, 0
			o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
			r := portsmocks.NewMockRenderer(t)
			s, c := fullscreenScene()
			s.Capture = tc.capture
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
			pipeline := capture.NewPipeline(context.Background(), nil)
			defer pipeline.Close(r)
			direct, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, pipeline)
			require.NoError(t, err)
			require.True(t, direct)
		})
	}
}

// A request for a hidden workspace with no child renderer factory
// fails closed; it is neither served from the displayed frame nor does it
// stop the output.
func TestSubmitFrameHiddenWorkspaceWithoutFactoryFailsClosed(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once() // the display only
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	defer pipeline.Close(r)
	scene := drmSessionScene()
	scene.CaptureScene = &ports.Scene{OutputWidth: 200, OutputHeight: 100, Scale: 1}
	scene.Capture.Workspace = 9
	requests := []ports.CaptureRequest{sessionRequest(t, 1, false)}
	requests[0].Workspace, requests[0].OffScreen = 9, true
	_, err := o.submitFrame(context.Background(), r, scene, nil, nil, requests, pipeline)
	require.NoError(t, err)
	require.ErrorIs(t, (<-replies).Err, capture.ErrOffscreenUnavailable)
}

// A hidden request whose scene carries no CaptureScene still goes to the
// hidden path, which fails it closed: it is neither dropped unanswered nor
// served from the displayed frame.
func TestSubmitFrameHiddenWorkspaceWithoutCaptureSceneFails(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once() // the display only
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	defer pipeline.Close(r)
	scene := drmSessionScene()
	scene.Capture.Workspace = 9
	requests := []ports.CaptureRequest{sessionRequest(t, 1, false)}
	requests[0].Workspace, requests[0].OffScreen = 9, true
	_, err := o.submitFrame(context.Background(), r, scene, nil, nil, requests, pipeline)
	require.NoError(t, err)
	select {
	case d := <-replies:
		require.ErrorIs(t, d.Err, capture.ErrOffscreenUnavailable)
	case <-time.After(3 * time.Second):
		t.Fatal("hidden request without CaptureScene was dropped unanswered")
	}
}

// A report caps only the windows an unfinished child render drew; the display's
// own windows are reported at their full content Seq, and the report goes on
// (nothing waits for the child).
func TestReportCapsPerWindowWhileChildReads(t *testing.T) {
	o, _, _ := testOutput(t)
	readEnd, writeEnd, err := os.Pipe() // the child's sync file: readable once it signals
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeEnd.Close() })
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(readEnd, nil).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	child.EXPECT().EndCapture(frame).Return().Maybe()
	child.EXPECT().Close().Return().Once()
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	pipeline.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	o.capHidden = pipeline.CapHiddenSeen
	scene := ports.Scene{Scale: 1, OutputWidth: 2, OutputHeight: 2,
		Capture:      &ports.SceneCapture{Workspace: 9},
		CaptureScene: &ports.Scene{Scale: 1, OutputWidth: 2, OutputHeight: 2, Windows: []ports.SceneWindow{{ID: 5, Rect: ports.Rect{W: 2, H: 2}}}}}
	surfaces := map[ports.WindowID]ports.SurfaceContent{5: {ID: 5, Seq: 2}}
	req := sessionRequest(t, 1, false)
	req.Workspace, req.OffScreen = 9, true
	req.Region, req.Width, req.Height = image.Rect(0, 0, 2, 2), 2, 2
	pipeline.SubmitHidden(scene, surfaces, []ports.CaptureRequest{req})

	seen := map[ports.WindowID]uint64{1: 9, 5: 7} // window 1 belongs to the display
	o.report(nil, seen)
	require.True(t, o.capped, "an outstanding child read is reported repeatedly")
	require.Equal(t, map[ports.WindowID]uint64{1: 9, 5: 2}, o.reports.Unsent()[0].Seen)
	require.Equal(t, uint64(7), seen[5], "the caller's map is untouched")
	require.Equal(t, map[ports.WindowID]uint64{5: 2}, o.reports.Unsent()[0].ChildReads)
	previous := o.reports.Unsent()[0].ChildReads
	allocs := testing.AllocsPerRun(100, func() { o.report(nil, seen) })
	require.LessOrEqual(t, allocs, float64(1), "only the fence poll may allocate")
	t.Logf("unchanged child report: %.1f allocs", allocs)

	_, err = writeEnd.Write([]byte{1}) // the child's GPU work finished
	require.NoError(t, err)
	o.report(nil, seen)
	require.False(t, o.capped)
	require.Equal(t, map[ports.WindowID]uint64{1: 9, 5: 7}, o.reports.Unsent()[0].Seen, "limit lifted")
	require.Empty(t, o.reports.Unsent()[0].ChildReads)
	require.Equal(t, map[ports.WindowID]uint64{5: 2}, previous, "sent snapshot remains immutable")
	pipeline.Close(nil)
}

// GO004: a hidden-workspace child is drawn on its own device: the display
// keeps direct scanout of a fullscreen window when no capture reads the
// displayed frame and the session draws no border on screen.
func TestHiddenWorkspaceChildKeepsDirectScanout(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor, o.tearing, o.vrrProp = nil, false, 0
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeEnd.Close() })
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(readEnd, nil).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	child.EXPECT().EndCapture(frame).Return().Maybe()
	child.EXPECT().Close().Return().Once()
	display := portsmocks.NewMockRenderer(t) // no Render: the display is scanned out
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	pipeline.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	o.capHidden = pipeline.CapHiddenSeen
	s, c := fullscreenScene()
	s.Capture = &ports.SceneCapture{Workspace: 9}
	s.CaptureScene = &ports.Scene{Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 5, Rect: ports.Rect{W: 200, H: 100}}}}
	req := sessionRequest(t, 1, false)
	req.Workspace, req.OffScreen = 9, true
	req.Region, req.Width, req.Height, req.Stride = image.Rect(0, 0, 200, 100), 200, 100, 800
	require.NoError(t, req.Dst.File.Truncate(800*100))
	direct, err := o.submitFrame(context.Background(), display, s, c, map[ports.WindowID]uint64{}, []ports.CaptureRequest{req}, pipeline)
	require.NoError(t, err)
	require.True(t, direct, "the displayed fullscreen window stays scanned out")
	require.True(t, o.frame.pendingCommit(), "display flip has not completed")
	require.Equal(t, map[ports.WindowID]uint64{5: 0}, o.reports.Unsent()[0].ChildReads, "child hold published before the display flip")
	require.Empty(t, o.reports.Unsent()[0].Seen, "display reads are not advanced early")
	require.NoError(t, (<-replies).Err)
	_, err = writeEnd.Write([]byte{1})
	require.NoError(t, err)
	pipeline.Close(display)
}

// Child holds do not expire, so a finished child read must lift its
// hold even while the display's own commit never completes. The display's
// Seen stays at the last safe value and the output is not stopped.
func TestRunChildHoldLiftedWhileDisplayCommitStalls(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t, nil, nil)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	o.frame.stuckAfter = 30 * time.Second // the display commit stays in flight
	o.flipped = make(chan flipEvent)      // no flip event ever
	display := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	display.EXPECT().SetHDR(float64(0)).Return().Maybe()
	display.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	display.EXPECT().UseTarget(mock.Anything).Return()
	display.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	display.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()

	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeEnd.Close() })
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(readEnd, nil).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	child.EXPECT().EndCapture(frame).Return().Maybe()
	child.EXPECT().Close().Return().Once()
	o.NewCaptureRenderer = func(int, int) (ports.Renderer, error) { return child, nil }

	scenes := make(chan ports.Scene)
	contents := make(chan ports.SurfaceContent) // unbuffered: taken before the scene
	captures := make(chan ports.CaptureRequest) // unbuffered: a send returns once Run took it
	captured := make(chan ports.CaptureDone, 4)
	presented := make(chan ports.OutputPresented, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return display, nil }, nil, make(chan bool), scenes, contents, nil, presented, captures, captured)
	}()
	count := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	waitFor(t, func() bool { return count() == 2 })

	contents <- ports.SurfaceContent{ID: 5, Seq: 2, Width: 1, Height: 1, SHM: &ports.SHMBuffer{}}
	// The request waits for the scene; once a display frame is pending no
	// request is served, so it must be queued before the scene arrives.
	req := sessionRequest(t, 1, false)
	req.Workspace, req.OffScreen = 9, true
	req.Region, req.Width, req.Height, req.Stride = image.Rect(0, 0, 200, 100), 200, 100, 800
	require.NoError(t, req.Dst.File.Truncate(800*100))
	captures <- req
	scenes <- ports.Scene{Seq: 1, Scale: 1, OutputWidth: 200, OutputHeight: 100,
		Capture:      &ports.SceneCapture{Workspace: 9},
		CaptureScene: &ports.Scene{Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 5, Rect: ports.Rect{W: 200, H: 100}}}}}

	next := func(match func(ports.OutputPresented) bool) ports.OutputPresented {
		t.Helper()
		timeout := time.After(2 * time.Second)
		for {
			select {
			case r := <-presented:
				if match(r) {
					return r
				}
			case <-timeout:
				t.Fatal("no matching report")
			}
		}
	}
	held := next(func(r ports.OutputPresented) bool { return len(r.ChildReads) > 0 })
	require.Equal(t, map[ports.WindowID]uint64{5: 2}, held.ChildReads)
	require.Equal(t, 3, count(), "display frame committed, never completes")

	// A newer buffer of a display window arrives while the display commit is
	// pending: the GPU may still read the older one, so it is not reported.
	contents <- ports.SurfaceContent{ID: 1, Seq: 9, Width: 1, Height: 1, SHM: &ports.SHMBuffer{}}
	_, err = writeEnd.Write([]byte{1}) // the child's GPU work finished
	require.NoError(t, err)
	lifted := next(func(r ports.OutputPresented) bool { return len(r.ChildReads) == 0 })
	require.Empty(t, lifted.ChildReads)
	require.NotContains(t, lifted.Seen, ports.WindowID(1), "display Seen does not advance while its commit is pending: only the child hold is lifted")
	require.Nil(t, lifted.Flip)
	require.Equal(t, 3, count(), "no extra commit")
	require.NoError(t, (<-captured).Err)
	cancel()
	require.NoError(t, <-done)
}

// A capture requested before the scene that shows its indicator is held: the
// frame on screen is not redrawn for it and nothing is copied. The first scene
// that shows the border draws the frame without it, copies that, then draws
// and commits the frame with the border.
func TestRunCaptureWaitsForItsIndicatorScene(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.tearing, o.cursor, o.fbs = false, nil, [2]uint32{}
	flips := make(chan flipEvent, 4)
	o.flipped = flips
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	// Only what the renderer draws for scenes after the modeset's black frame.
	var mu sync.Mutex
	var order []string
	note := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		switch {
		case s.OutputWidth == 0:
			// The modeset's black frame.
		case s.Seq == 0:
			note("plain")
		case len(s.CaptureIndicators) > 0:
			note("indicated")
		default:
			note("bare")
		}
		return nil, nil
	})
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().RunAndReturn(func() (ports.CaptureFrame, error) {
		note("copy")
		return frame, nil
	}).Once()
	r.EXPECT().EndCapture(frame).Return().Maybe()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()

	scenes := make(chan ports.Scene, 2)
	captures := make(chan ports.CaptureRequest)
	captured := make(chan ports.CaptureDone, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, nil, nil, make(chan ports.OutputPresented, 16), captures, captured)
	}()
	count := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	last := func() commitRec {
		commitMu.Lock()
		defer commitMu.Unlock()
		return (*commits)[len(*commits)-1]
	}
	waitFor(t, func() bool { return count() == 2 }) // modeset
	bare := ports.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2, Windows: []ports.SceneWindow{{ID: 1}}}
	scenes <- bare
	waitFor(t, func() bool { return count() == 3 })
	flips <- eventOf(last())

	req := sessionRequest(t, 1, false)
	req.Indicate = true
	captures <- req
	select {
	case d := <-captured:
		t.Fatalf("served from a scene without the indicator: %+v", d)
	case <-time.After(60 * time.Millisecond):
	}
	require.Equal(t, 3, count(), "no frame is drawn for a held request")

	indicated := bare
	indicated.Seq = 2
	indicated.CaptureIndicators = []ports.CaptureIndicator{{Rect: ports.Rect{W: 2, H: 2}}}
	scenes <- indicated
	waitFor(t, func() bool { return count() == 4 })
	select {
	case d := <-captured:
		require.NoError(t, d.Err)
	case <-time.After(2 * time.Second):
		t.Fatal("not served once the indicator scene exists")
	}
	flips <- eventOf(last())
	cancel()
	require.NoError(t, <-done)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"bare", "plain", "copy", "indicated"}, order, "copied from the frame without the indicator, committed together with the indicated frame")
}

// A request whose indicator never comes fails after the bounded wait, and no
// frame is drawn or copied for it.
func TestRunCaptureWithoutIndicatorFailsAfterTheBoundedWait(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.tearing, o.cursor, o.fbs = false, nil, [2]uint32{}
	flips := make(chan flipEvent, 2)
	o.flipped = flips
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	scenes := make(chan ports.Scene, 1)
	captures := make(chan ports.CaptureRequest)
	captured := make(chan ports.CaptureDone, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, nil, nil, make(chan ports.OutputPresented, 16), captures, captured)
	}()
	count := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	waitFor(t, func() bool { return count() == 2 })
	scenes <- ports.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}
	waitFor(t, func() bool { return count() == 3 })
	commitMu.Lock()
	flips <- eventOf((*commits)[2])
	commitMu.Unlock()
	start := time.Now()
	req := sessionRequest(t, 1, false)
	req.Indicate = true
	captures <- req
	select {
	case d := <-captured:
		require.ErrorIs(t, d.Err, capture.ErrIndicatorMissing)
		require.GreaterOrEqual(t, time.Since(start), capture.HoldFor-20*time.Millisecond)
	case <-time.After(3 * time.Second):
		t.Fatal("a held request was never answered")
	}
	require.Equal(t, 3, count())
	cancel()
	require.NoError(t, <-done)
}

func indicatedScene() ports.Scene {
	return ports.Scene{Seq: 4, OutputWidth: 200, OutputHeight: 100, Scale: 1, Windows: []ports.SceneWindow{{ID: 1}},
		CaptureIndicators: []ports.CaptureIndicator{{Rect: ports.Rect{W: 200, H: 100}}}}
}

// A capture is delivered only when the frame that carries its indicator is
// committed: the plain frame is rendered and copied, but the pixels stay
// behind the gate and the capture fails when that frame fails to render or to
// commit. The mock frame has no Read: a delivery would panic.
func TestSubmitFrameFailedIndicatedFrameDeliversNothing(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		renders []error // plain, then indicated
		commit  []error
	}{
		{"render error", []error{nil, boom}, nil},
		{"commit error", []error{nil, nil}, []error{boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := testOutput(t, tc.commit...)
			o.cursor = nil
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			for _, err := range tc.renders {
				r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, err).Once()
			}
			frame := portsmocks.NewMockCaptureFrame(t)
			frame.EXPECT().Done().Return(nil).Maybe()
			r.EXPECT().BeginCapture().Return(frame, nil).Once()
			r.EXPECT().EndCapture(frame).Return().Once()
			replies := make(chan ports.CaptureDone, 4)
			pipeline := capture.NewPipeline(context.Background(), replies)
			q := sessionRequest(t, 1, false)
			_, err := o.submitFrame(context.Background(), r, indicatedScene(), nil, nil, []ports.CaptureRequest{q}, pipeline)
			require.Error(t, err)
			var done ports.CaptureDone
			select {
			case done = <-replies:
			case <-time.After(5 * time.Second):
				t.Fatal("no reply")
			}
			pipeline.Close(r)
			require.ErrorIs(t, done.Err, capture.ErrIndicatorMissing)
			require.ErrorIs(t, done.Err, boom)
		})
	}
}

// The same frame committed: the capture is delivered.
func TestSubmitFrameIndicatedFrameCommittedDelivers(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Twice()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	var reads atomic.Int32
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).RunAndReturn(func(image.Rectangle, []byte, int) error {
		reads.Add(1)
		return nil
	}).Maybe()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 4)
	pipeline := capture.NewPipeline(context.Background(), replies)
	_, err := o.submitFrame(context.Background(), r, indicatedScene(), nil, nil, []ports.CaptureRequest{sessionRequest(t, 1, false)}, pipeline)
	require.NoError(t, err)
	select {
	case done := <-replies:
		require.NoError(t, done.Err)
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
	pipeline.Close(r)
	require.Equal(t, int32(1), reads.Load())
}
