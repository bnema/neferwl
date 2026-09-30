package capture

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func cleanTestScene() ports.Scene {
	return ports.Scene{
		Seq: 5,
		Windows: []ports.SceneWindow{
			{ID: 1}, {ID: 20, Popup: true, OverLayers: true}, {ID: 21, Popup: true, OverLayers: true}, {ID: 3},
		},
		Layers: []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop}, {ID: 11, Layer: ports.LayerTop}, {ID: 12, Layer: ports.LayerOverlay}},
		Capture: &ports.SceneCapture{
			Session: 7, BorderWidth: 2, BorderColor: ports.CaptureBorderColor,
			Excluded: []ports.WindowID{10, 20},
		},
	}
}

func TestCleanSceneDropsOnlyExcludedAndBorder(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	s := cleanTestScene()
	c := p.CleanScene(s)
	require.Nil(t, c.Capture)
	require.Zero(t, c.Seq)
	require.Equal(t, []ports.SceneWindow{{ID: 1}, {ID: 21, Popup: true, OverLayers: true}, {ID: 3}}, c.Windows)
	require.Equal(t, []ports.SceneLayer{{ID: 11, Layer: ports.LayerTop}, {ID: 12, Layer: ports.LayerOverlay}}, c.Layers)
	// The displayed scene is not mutated.
	require.Len(t, s.Windows, 4)
	require.Len(t, s.Layers, 3)
	require.NotNil(t, s.Capture)
	require.Equal(t, uint64(5), s.Seq)
}

func TestCleanSceneWithoutSessionUnchangedNoAlloc(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture = nil
	require.Equal(t, s, p.CleanScene(s))
	reqs := make([]ports.CaptureRequest, 3)
	for i := range reqs {
		reqs[i].ID = uint64(i + 1)
	}
	if allocs := testing.AllocsPerRun(100, func() {
		_ = p.CleanScene(s)
		p.Split(s, reqs)
	}); allocs != 0 {
		t.Fatalf("no session: %.1f allocs, want 0", allocs)
	}
}

func TestCleanSceneWarmedNoAlloc(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	s := cleanTestScene()
	p.CleanScene(s)
	if allocs := testing.AllocsPerRun(100, func() { _ = p.CleanScene(s) }); allocs != 0 {
		t.Fatalf("warmed clean scene: %.1f allocs, want 0", allocs)
	}
}

func TestSplitOrdersAndFailsClosed(t *testing.T) {
	replies := make(chan ports.CaptureDone, 8)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := cleanTestScene()
	reqs := []ports.CaptureRequest{
		{ID: 1, Clean: true, Session: 7},
		{ID: 2},
		{ID: 3, Clean: true, Session: 8}, // another session: never served
		{ID: 4, Clean: true, Session: 7},
		{ID: 5},
	}
	normal, clean := p.Split(s, reqs)
	ids := func(q []ports.CaptureRequest) (out []uint64) {
		for _, r := range q {
			out = append(out, r.ID)
		}
		return out
	}
	require.ElementsMatch(t, []uint64{2, 5}, ids(normal))
	require.ElementsMatch(t, []uint64{1, 4}, ids(clean))
	for _, q := range clean {
		require.True(t, q.Clean)
	}
	for _, q := range normal {
		require.False(t, q.Clean)
	}
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(3), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionInactive)
	for _, q := range reqs[len(normal)+len(clean):] {
		require.True(t, Handed(q), "tail must be zeroed")
	}
}

func TestSplitCleanWithoutSceneSessionFails(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture = nil
	normal, clean := p.Split(s, []ports.CaptureRequest{{ID: 1, Clean: true, Session: 7}, {ID: 2}})
	require.Len(t, normal, 1)
	require.Equal(t, uint64(2), normal[0].ID)
	require.Empty(t, clean)
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(1), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionInactive)
}

// A session with nothing to hide (no border, no excluded surface in the
// scene) is served from the displayed frame: no second composition.
func TestSplitNoSecondCompositionWhenCleanEqualsDisplayed(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture.BorderWidth, s.Capture.Excluded = 0, []ports.WindowID{99}
	normal, clean := p.Split(s, []ports.CaptureRequest{{ID: 1, Clean: true, Session: 7}, {ID: 2}})
	require.Len(t, normal, 2)
	require.Empty(t, clean)
}

func TestSplitStandardCapturesKeepSessionScene(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	normal, clean := p.Split(cleanTestScene(), []ports.CaptureRequest{{ID: 1}, {ID: 2}})
	require.Len(t, normal, 2)
	require.Empty(t, clean)
}

// A request stamped with a revision the scene has not reached is refused:
// the scene's exclusion list may predate a newly mapped HUD layer.
func TestSplitStaleRevisionFailsClosed(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture.Revision = 3
	normal, clean := p.Split(s, []ports.CaptureRequest{
		{ID: 1, Clean: true, Session: 7, CaptureRevision: 4},
		{ID: 2, Clean: true, Session: 7, CaptureRevision: 3},
		{ID: 3, Clean: true, Session: 7, CaptureRevision: 2},
	})
	require.Empty(t, normal)
	require.Len(t, clean, 2)
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(1), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionStale)
}
