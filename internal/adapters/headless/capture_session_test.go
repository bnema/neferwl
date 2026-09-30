package headless

import (
	"context"
	"errors"
	"image"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func sessionCaptureFile(t *testing.T) ports.SHMBuffer {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "capture")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, f.Truncate(16))
	return ports.SHMBuffer{File: f}
}

// runSession drives one frame with the given requests and returns the
// renderer event order and the replies.
func runSession(t *testing.T, scene ports.Scene, reqs []ports.CaptureRequest, wantReplies int) ([]string, []ports.CaptureDone) {
	t.Helper()
	ev := &events{}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		// Clean scenes are renumbered 0; the tests' displayed scenes are not.
		if s.Seq == 0 {
			ev.add("render:clean")
		} else {
			ev.add("render:full")
		}
		return nil, nil
	}).Maybe()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().RunAndReturn(func() (ports.CaptureFrame, error) {
		ev.add("begin")
		return frame, nil
	}).Maybe()
	r.EXPECT().EndCapture(frame).Return().Maybe()
	r.EXPECT().Close().Return().Once()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scenes := make(chan ports.Scene, 1)
	incoming := make(chan ports.CaptureRequest)
	replies := make(chan ports.CaptureDone, 8)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }, Captured: replies}, scenes, nil, nil, incoming)
	}()
	// Requests wait for a scene: all of them belong to its first frame.
	for _, q := range reqs {
		q.Region, q.Width, q.Height, q.Stride, q.Format = image.Rect(0, 0, 2, 2), 2, 2, 8, 1
		q.Dst = sessionCaptureFile(t)
		incoming <- q
	}
	scenes <- scene
	var got []ports.CaptureDone
	for range wantReplies {
		select {
		case d := <-replies:
			got = append(got, d)
		case <-time.After(3 * time.Second):
			t.Fatal("missing capture reply")
		}
	}
	cancel()
	require.NoError(t, <-done)
	return ev.get(), got
}

func sessionScene() ports.Scene {
	return ports.Scene{Seq: 2, Windows: []ports.SceneWindow{{ID: 1}}, Layers: []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop}},
		Capture: &ports.SceneCapture{Session: 7, Excluded: []ports.WindowID{10}, BorderWidth: 2, BorderColor: ports.CaptureBorderColor}}
}

func TestCleanCaptureOrderedBeforeDisplayedFrame(t *testing.T) {
	order, replies := runSession(t, sessionScene(), []ports.CaptureRequest{{ID: 1, Clean: true, Session: 7}, {ID: 2}}, 2)
	// Clean composition and its copy come first; the displayed frame and
	// the standard capture (HUD and border included) follow.
	require.Equal(t, []string{"render:clean", "begin", "render:full", "begin"}, order)
	for _, d := range replies {
		require.NoError(t, d.Err)
	}
}

func TestCleanCaptureFailsClosedWithoutMatchingSession(t *testing.T) {
	scene := sessionScene()
	scene.Capture = nil
	order, replies := runSession(t, scene, []ports.CaptureRequest{{ID: 1, Clean: true, Session: 7}, {ID: 2}}, 2)
	require.Equal(t, []string{"render:full", "begin"}, order) // one frame, the standard capture only
	byID := map[uint64]error{}
	for _, d := range replies {
		byID[d.ID] = d.Err
	}
	require.ErrorIs(t, byID[1], capture.ErrSessionInactive)
	require.NoError(t, byID[2])
}

func TestStandardCaptureDuringSessionUsesOneComposition(t *testing.T) {
	order, replies := runSession(t, sessionScene(), []ports.CaptureRequest{{ID: 2}}, 1)
	require.Equal(t, []string{"render:full", "begin"}, order)
	require.NoError(t, replies[0].Err)
}

// A hidden workspace is drawn by the child renderer (made by the factory),
// not by the display's; the display still renders the scene it shows.
func TestHiddenWorkspaceCaptureUsesChildRenderer(t *testing.T) {
	read, write, err := os.Pipe()
	require.NoError(t, err)
	defer write.Close()
	display := portsmocks.NewMockRenderer(t)
	display.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	display.EXPECT().Close().Return().Once()
	child := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	child.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		require.Nil(t, s.Capture)
		require.Len(t, s.Layers, 0, "HUD left out of the child frame")
		return read, nil
	}).Once()
	child.EXPECT().BeginCapture().Return(frame, nil).Once()
	child.EXPECT().EndCapture(frame).Return().Maybe()
	child.EXPECT().Close().Return().Once()
	scene := sessionScene()
	scene.OutputWidth, scene.OutputHeight, scene.Scale = 2, 2, 1
	scene.Capture.TargetRect = ports.Rect{W: 2, H: 2}
	scene.CaptureScene = &ports.Scene{OutputWidth: 2, OutputHeight: 2, Scale: 1, Windows: []ports.SceneWindow{{ID: 5}}, Layers: []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scenes := make(chan ports.Scene, 1)
	incoming := make(chan ports.CaptureRequest)
	replies := make(chan ports.CaptureDone, 2)
	done := make(chan error, 1)
	var made [2]int
	presented := make(chan ports.OutputPresented, 4)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, Captured: replies, Presented: presented,
			NewRenderer:        func(int, int) (ports.Renderer, error) { return display, nil },
			NewCaptureRenderer: func(w, h int) (ports.Renderer, error) { made = [2]int{w, h}; return child, nil },
		}, scenes, nil, nil, incoming)
	}()
	q := ports.CaptureRequest{ID: 1, Clean: true, Session: 7, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: sessionCaptureFile(t)}
	incoming <- q
	scenes <- scene
	// The owner may report an idle frame before receiving the first scene.
	deadline := time.After(3 * time.Second)
waitHold:
	for {
		select {
		case report := <-presented:
			if len(report.ChildReads) > 0 {
				require.Equal(t, map[ports.WindowID]uint64{5: 0}, report.ChildReads, "hold published before waiting")
				break waitHold
			}
		case <-deadline:
			t.Fatal("no child hold report")
		}
	}
	select {
	case <-presented:
		t.Fatal("child hold lifted before its fence signalled")
	case <-time.After(120 * time.Millisecond): // beyond Wayland's 100ms timeout
	}
	_, err = write.Write([]byte{1})
	require.NoError(t, err)
	select {
	case report := <-presented:
		require.Empty(t, report.ChildReads, "completed render lifts child hold")
	case <-time.After(3 * time.Second):
		t.Fatal("no completed render report")
	}
	select {
	case d := <-replies:
		require.NoError(t, d.Err)
	case <-time.After(3 * time.Second):
		t.Fatal("no reply")
	}
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, [2]int{2, 2}, made)
}

// GO007: a render error while a clean request was already handed to a worker
// must not answer it a second time as request 0.
func TestRenderErrorDoesNotAnswerHandedRequestsAgain(t *testing.T) {
	display := portsmocks.NewMockRenderer(t)
	boom := errors.New("boom")
	display.EXPECT().Render(mock.MatchedBy(func(s ports.Scene) bool { return s.Seq == 0 }), mock.Anything).Return(nil, nil).Once() // clean frame
	display.EXPECT().Render(mock.MatchedBy(func(s ports.Scene) bool { return s.Seq != 0 }), mock.Anything).Return(nil, boom).Once()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	display.EXPECT().BeginCapture().Return(frame, nil).Once()
	display.EXPECT().EndCapture(frame).Return().Maybe()
	display.EXPECT().Close().Return().Once()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scenes := make(chan ports.Scene, 1)
	incoming := make(chan ports.CaptureRequest)
	replies := make(chan ports.CaptureDone, 8)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, Captured: replies,
			NewRenderer: func(int, int) (ports.Renderer, error) { return display, nil }}, scenes, nil, nil, incoming)
	}()
	incoming <- ports.CaptureRequest{ID: 1, Clean: true, Session: 7, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: sessionCaptureFile(t)}
	scenes <- sessionScene()
	select {
	case err := <-done:
		require.ErrorIs(t, err, boom)
	case <-time.After(3 * time.Second):
		t.Fatal("output did not stop on the render error")
	}
	var ids []uint64
	for len(replies) > 0 {
		ids = append(ids, (<-replies).ID)
	}
	require.Equal(t, []uint64{1}, ids, "request 1 answered once by its worker, no pseudo request 0")
}
