package capture

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func hiddenScene() ports.Scene {
	return ports.Scene{
		Scale: 2, OutputWidth: 100, OutputHeight: 50, Windows: []ports.SceneWindow{{ID: 1}},
		Capture: &ports.SceneCapture{Workspace: 9},
		CaptureScene: &ports.Scene{
			OutputWidth: 100, OutputHeight: 50, Scale: 2, Seq: 9,
			Windows: []ports.SceneWindow{{ID: 5}, {ID: 10, Popup: true}},
			Layers:  []ports.SceneLayer{{ID: 10}, {ID: 11}},
		},
	}
}

func hiddenRequest(t *testing.T, id uint64, region image.Rectangle) ports.CaptureRequest {
	q := pipelineRequest(t, id)
	q.Workspace, q.OffScreen = 9, true
	q.Region, q.Width, q.Height, q.Stride = region, region.Dx(), region.Dy(), region.Dx()*4
	require.NoError(t, q.Dst.File.Truncate(int64(q.Stride*q.Height)))
	return q
}

func childMock(t *testing.T, w, h int) (*portsmocks.MockRenderer, *portsmocks.MockCaptureFrame) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	r.EXPECT().EndCapture(frame).Return().Maybe()
	r.EXPECT().Close().Return().Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	return r, frame
}

func TestSubmitHiddenDrawsChildAndRebasesRegion(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	child, _ := childMock(t, 200, 100)
	var scene ports.Scene
	child.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		scene = s
		return nil, nil
	}).Once()
	var made [2]int
	p.EnableOffscreen(func(w, h int) (ports.Renderer, error) { made = [2]int{w, h}; return child, nil })
	s := hiddenScene()
	p.SubmitHidden(s, nil, []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(480, 360, 680, 460))})
	require.Equal(t, [2]int{200, 100}, made, "child image is the frame in physical pixels")
	require.Nil(t, scene.Capture)
	require.Zero(t, scene.Seq)
	require.Equal(t, []ports.SceneWindow{{ID: 5}, {ID: 10, Popup: true}}, scene.Windows)
	require.Equal(t, []ports.SceneLayer{{ID: 10}, {ID: 11}}, scene.Layers)
	require.NoError(t, awaitCapture(t, replies).Err)
	p.Close(nil)
}

func TestSubmitHiddenFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ports.Scene)
		want error
	}{
		{"output off", func(s *ports.Scene) { s.Off = true }, nil},
		{"no hidden workspace in the scene", func(s *ports.Scene) { s.Capture.Workspace = 0 }, ErrOffscreenUnavailable},
		{"frame too large", func(s *ports.Scene) { s.CaptureScene.OutputWidth = 20000 }, ErrOffscreenGeometry},
		{"frame empty", func(s *ports.Scene) { s.CaptureScene.OutputWidth = 0 }, ErrOffscreenGeometry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replies := make(chan ports.CaptureDone, 2)
			p := NewPipeline(context.Background(), replies)
			defer p.Close(nil)
			p.EnableOffscreen(func(int, int) (ports.Renderer, error) { t.Fatal("factory called"); return nil, nil })
			s := hiddenScene()
			tc.edit(&s)
			p.SubmitHidden(s, nil, []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(0, 0, 200, 100))})
			got := awaitCapture(t, replies)
			require.Error(t, got.Err)
			if tc.want != nil {
				require.ErrorIs(t, got.Err, tc.want)
			}
		})
	}
}

func TestSubmitHiddenFactoryFailureAndNoFactory(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	p.SubmitHidden(hiddenScene(), nil, []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(0, 0, 200, 100))})
	require.ErrorIs(t, awaitCapture(t, replies).Err, ErrOffscreenUnavailable)
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return nil, errors.New("no gpu") })
	p.SubmitHidden(hiddenScene(), nil, []ports.CaptureRequest{hiddenRequest(t, 2, image.Rect(0, 0, 200, 100))})
	require.ErrorIs(t, awaitCapture(t, replies).Err, ErrOffscreenUnavailable)
}

func TestSubmitHiddenRegionOutsideFrameFailsOthersServed(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	child, _ := childMock(t, 200, 100)
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	p.SubmitHidden(hiddenScene(), nil, []ports.CaptureRequest{
		hiddenRequest(t, 1, image.Rect(0, 0, 300, 100)), // not the child's size
		hiddenRequest(t, 2, image.Rect(0, 0, 200, 100)),
	})
	byID := map[uint64]error{}
	for range 2 {
		d := awaitCapture(t, replies)
		byID[d.ID] = d.Err
	}
	require.ErrorIs(t, byID[1], ErrOffscreenGeometry)
	require.NoError(t, byID[2])
	p.Close(nil)
}

// A hidden-workspace capture submitted while the indicator gate is open is held
// by the child pipeline: no reply until the owner's verdict, then it is
// released (nil) or failed (error) like a displayed capture.
func TestSubmitHiddenHeldByGateUntilVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict error
	}{
		{"released", nil},
		{"failed", GateVerdict(errors.New("frame dropped"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replies := make(chan ports.CaptureDone, 4)
			p := NewPipeline(context.Background(), replies)
			child, _ := childMock(t, 200, 100)
			child.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
			p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
			p.BeginGate()
			p.SubmitHidden(hiddenScene(), nil, []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(0, 0, 200, 100))})
			select {
			case d := <-replies:
				t.Fatalf("answered before the gate verdict: %v", d.Err)
			case <-time.After(100 * time.Millisecond):
			}
			p.EndGate(tc.verdict)
			got := awaitCapture(t, replies)
			if tc.verdict == nil {
				require.NoError(t, got.Err)
			} else {
				require.ErrorIs(t, got.Err, ErrIndicatorMissing)
			}
			p.Close(nil)
		})
	}
}

func TestSubmitHiddenChildClosesWhenSessionEnds(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	child, _ := childMock(t, 200, 100)
	child.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	p.SubmitHidden(hiddenScene(), nil, []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(0, 0, 200, 100))})
	require.NoError(t, awaitCapture(t, replies).Err)
	// Wait for the worker to hand the lease back, then the session ends.
	b := <-p.HiddenCompleted()
	p.RecycleHidden(b)
	p.Retire(ports.Scene{})
	require.Nil(t, p.off.r, "child closed once the scene has no CaptureScene")
	p.Close(nil)
}

func TestHiddenNilPipelineSafe(t *testing.T) {
	var p *Pipeline
	require.False(t, p.HiddenReading())
	p.Retire(ports.Scene{})
}
