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
		Layers:  []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop}, {ID: 11, Layer: ports.LayerTop}, {ID: 12, Layer: ports.LayerOverlay}},
		Capture: &ports.SceneCapture{Session: 7, Excluded: []ports.WindowID{10, 20}},
	}
}

func ids(q []ports.CaptureRequest) (out []uint64) {
	for _, r := range q {
		out = append(out, r.ID)
	}
	return out
}

func TestCleanSceneDropsOnlyExcluded(t *testing.T) {
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

func TestSplitNoAlloc(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	for name, s := range map[string]ports.Scene{"no capture": {Windows: cleanTestScene().Windows}, "exclusion": cleanTestScene()} {
		reqs := make([]ports.CaptureRequest, 3)
		for i := range reqs {
			reqs[i].ID = uint64(i + 1)
		}
		if allocs := testing.AllocsPerRun(100, func() {
			_ = p.CleanScene(s)
			p.Split(s, reqs)
		}); allocs != 0 {
			t.Fatalf("%s: %.1f allocs, want 0", name, allocs)
		}
	}
}

func TestSplitOrdersAndFailsClosed(t *testing.T) {
	replies := make(chan ports.CaptureDone, 8)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture.Revision = 1
	reqs := []ports.CaptureRequest{
		{ID: 1, Exclude: true, Session: 7, CaptureRevision: 1},
		{ID: 2},
		{ID: 3, Exclude: true, Session: 8, CaptureRevision: 1}, // another session: never served
		{ID: 4, Exclude: true, Session: 7, CaptureRevision: 1},
		{ID: 5},
	}
	normal, clean, hidden := p.Split(s, reqs)
	require.ElementsMatch(t, []uint64{2, 5}, ids(normal))
	require.ElementsMatch(t, []uint64{1, 4}, ids(clean))
	require.Empty(t, hidden)
	for _, q := range clean {
		require.True(t, q.Exclude)
	}
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(3), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionInactive)
	for _, q := range reqs[len(normal)+len(clean):] {
		require.True(t, Handed(q), "tail must be zeroed")
	}
}

func TestSplitExcludeWithoutSceneExclusionFails(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture = nil
	normal, clean, _ := p.Split(s, []ports.CaptureRequest{{ID: 1, Exclude: true, Session: 7}, {ID: 2}})
	require.Equal(t, []uint64{2}, ids(normal))
	require.Empty(t, clean)
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(1), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionInactive)
}

// An exclusion with nothing to hide in the scene is served from the displayed
// frame: no second composition.
func TestSplitNoSecondCompositionWhenNothingExcludedIsShown(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	s := cleanTestScene()
	s.Capture.Excluded = []ports.WindowID{99}
	normal, clean, _ := p.Split(s, []ports.CaptureRequest{{ID: 1, Exclude: true, Session: 7}, {ID: 2}})
	require.Len(t, normal, 2)
	require.Empty(t, clean)
}

// Every other capture keeps the HUD, even while an exclusion is live.
func TestSplitStandardCapturesKeepExclusionScene(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 1))
	defer p.Close(nil)
	normal, clean, _ := p.Split(cleanTestScene(), []ports.CaptureRequest{{ID: 1}, {ID: 2}})
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
	normal, clean, _ := p.Split(s, []ports.CaptureRequest{
		{ID: 1, Exclude: true, Session: 7, CaptureRevision: 4},
		{ID: 2, Exclude: true, Session: 7, CaptureRevision: 3},
		{ID: 3, Exclude: true, Session: 7, CaptureRevision: 2},
	})
	require.Empty(t, normal)
	require.Len(t, clean, 2)
	got := awaitCapture(t, replies)
	require.Equal(t, uint64(1), got.ID)
	require.ErrorIs(t, got.Err, ErrSessionStale)
}

// A workspace request is served only if its workspace is still where the
// request expected: on screen (Shown), or the one rendered off screen.
func TestSplitWorkspaceMovedFailsClosed(t *testing.T) {
	replies := make(chan ports.CaptureDone, 8)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	s := ports.Scene{Capture: &ports.SceneCapture{Shown: 3, Workspace: 4}}
	reqs := []ports.CaptureRequest{
		{ID: 1, Workspace: 3},                  // on screen, still shown
		{ID: 2, Workspace: 4, OffScreen: true}, // off screen, still rendered
		{ID: 3, Workspace: 4},                  // was on screen, now hidden
		{ID: 4, Workspace: 3, OffScreen: true}, // was hidden, now shown
		{ID: 5, Workspace: 9, OffScreen: true}, // not rendered at all
		{ID: 6},
	}
	normal, clean, hidden := p.Split(s, reqs)
	require.ElementsMatch(t, []uint64{1, 6}, ids(normal))
	require.Empty(t, clean)
	require.Equal(t, []uint64{2}, ids(hidden))
	failed := map[uint64]bool{}
	for range 3 {
		d := awaitCapture(t, replies)
		require.ErrorIs(t, d.Err, ErrWorkspaceMoved)
		failed[d.ID] = true
	}
	require.Equal(t, map[uint64]bool{3: true, 4: true, 5: true}, failed)
	// Without any capture state no workspace request is served.
	normal, _, hidden = p.Split(ports.Scene{}, []ports.CaptureRequest{{ID: 7, Workspace: 3}})
	require.Empty(t, normal)
	require.Empty(t, hidden)
	require.ErrorIs(t, awaitCapture(t, replies).Err, ErrWorkspaceMoved)
}
