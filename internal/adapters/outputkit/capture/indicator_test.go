package capture

import (
	"context"
	"image"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func indicatorScene() domain.Scene {
	s := excludedTestScene()
	s.Capture = nil
	s.CaptureIndicators = []domain.CaptureIndicator{{Rect: domain.Rect{W: 10, H: 10}}, {Pill: true, Rect: domain.Rect{X: 1, Y: 1, W: 3, H: 3}}}
	return s
}

// No capture scene ever carries the indicator: excluded scenes, plain scenes
// and the off-screen child are derived without it.
func TestCaptureScenesNeverCarryTheIndicator(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan domain.CaptureDone, 1))
	defer p.Close(nil)
	s := indicatorScene()
	require.Nil(t, p.ExcludedScene(s).CaptureIndicators)
	s.Capture = excludedTestScene().Capture
	require.Nil(t, p.ExcludedScene(s).CaptureIndicators)
	require.Nil(t, plain(s).CaptureIndicators)
	require.Len(t, s.CaptureIndicators, 2, "the displayed scene keeps its indicator")
	// Even an exclusion-less request is fine: it is drawn from the plain frame.
	normal, excluded, _ := p.Split(s, []domain.CaptureRequest{{ID: 1}, {ID: 2, Exclude: true, Session: 7}})
	require.Equal(t, []uint64{1}, ids(normal))
	require.Equal(t, []uint64{2}, ids(excluded), "an Exclude request is drawn from ExcludedScene, never the displayed frame")
}

// The requests of the displayed frame are answered from a frame rendered
// without the indicator before the displayed one.
func TestSubmitPlainRendersWithoutIndicatorFirst(t *testing.T) {
	replies := make(chan domain.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return((*os.File)(nil))
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
	var rendered []domain.Scene
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, _ map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
		rendered = append(rendered, s)
		return nil, nil
	}).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	q := pipelineRequest(t, 1)
	reqs := []domain.CaptureRequest{q}
	took, done, err := p.SubmitPlain(r, indicatorScene(), nil, reqs)
	require.NoError(t, err)
	require.Nil(t, done)
	require.True(t, took)
	require.True(t, Handed(reqs[0]))
	require.Len(t, rendered, 1)
	require.Nil(t, rendered[0].CaptureIndicators)
	require.Zero(t, rendered[0].Seq)
	require.Equal(t, uint64(1), awaitCapture(t, replies).ID)
	p.Close(r)
	frame.Calls = nil
}

// Without indicator, or without requests, nothing is rendered and nothing
// allocates: the feature is free while no capture is shown.
func TestSubmitPlainIdleCostsNothing(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan domain.CaptureDone, 1))
	defer p.Close(nil)
	r := portsmocks.NewMockRenderer(t)
	s := excludedTestScene()
	reqs := []domain.CaptureRequest{{ID: 1}}
	if took, _, err := p.SubmitPlain(r, s, nil, reqs); took || err != nil {
		t.Fatalf("took %v err %v without an indicator", took, err)
	}
	if took, _, err := p.SubmitPlain(r, indicatorScene(), nil, nil); took || err != nil {
		t.Fatalf("took %v err %v without requests", took, err)
	}
	if n := testing.AllocsPerRun(100, func() { _, _, _ = p.SubmitPlain(r, s, nil, reqs) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// A scene that carries the indicator is still gated: once the session is
// protected its plain frame is never copied, and the requests fail with the
// security error, closed, exactly once.
func TestSubmitPlainRejectsProtectedEpoch(t *testing.T) {
	locked := domain.SecurityState{Generation: 3, Protected: true}
	replies := make(chan domain.CaptureDone, 2)
	p := NewPipeline(context.Background(), replies)
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().Return(locked).Maybe()
	p.Security = security
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	q := pipelineRequest(t, 1)
	file := q.Dst.File
	s := indicatorScene()
	s.Security = locked
	took, _, err := p.SubmitPlain(r, s, nil, []domain.CaptureRequest{q})
	require.NoError(t, err)
	require.True(t, took)
	p.Close(r)
	require.Len(t, replies, 1)
	require.ErrorIs(t, (<-replies).Err, ErrSecurityState)
	r.AssertNotCalled(t, "BeginCapture")
	_, statErr := file.Stat()
	require.ErrorIs(t, statErr, os.ErrClosed)
}

// Hint labels are never captured either: a scene with only labels is served
// from the plain frame, and an Exclude request is ordered to the excluded scene.
func TestSubmitPlainServesHintLabelScenesWithoutLabels(t *testing.T) {
	replies := make(chan domain.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return((*os.File)(nil))
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
	var rendered []domain.Scene
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, _ map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
		rendered = append(rendered, s)
		return nil, nil
	}).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	s := excludedTestScene()
	s.Capture = nil
	s.Overlays = []domain.Overlay{{ID: 1, Seq: 1, Kind: domain.OverlayLabel, Text: "f"}}
	reqs := []domain.CaptureRequest{pipelineRequest(t, 1)}
	require.False(t, reqs[0].Indicate)
	took, done, err := p.SubmitPlain(r, s, nil, reqs)
	require.NoError(t, err)
	require.Nil(t, done)
	require.True(t, took)
	require.Len(t, rendered, 1)
	require.Nil(t, rendered[0].Overlays)
	require.Zero(t, rendered[0].Seq)
	require.Equal(t, uint64(1), awaitCapture(t, replies).ID)
	p.Close(r)
	frame.Calls = nil

	withSession := s
	withSession.Capture = &domain.SceneCapture{Session: 7}
	normal, excluded, _ := p.Split(withSession, []domain.CaptureRequest{{ID: 2, Exclude: true, Session: 7}})
	require.Empty(t, normal)
	require.Equal(t, []uint64{2}, ids(excluded), "labels force the Exclude request to the excluded scene")
	require.Nil(t, p.ExcludedScene(s).Overlays)
}
