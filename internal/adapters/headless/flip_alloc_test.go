package headless

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// Steady-state flip reporting: the Shows snapshot is reused while it does not
// change, and a changed one never mutates what an earlier flip handed out.
func TestFlipShowsAllocations(t *testing.T) {
	var f flipShows
	scene := ports.Scene{OutputWidth: 10, OutputHeight: 10, Windows: []ports.SceneWindow{{ID: 1, Rect: domain.Rect{W: 10, H: 10}}}}
	contents := map[domain.WindowID]ports.SurfaceContent{1: {ID: 1, Seq: 1}, 2: {ID: 2, Seq: 2}}
	first := f.flipInfo(scene, contents, nil)
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, first.Shows)

	require.Zero(t, testing.AllocsPerRun(100, func() { f.of(scene, contents, nil) }), "unchanged shows")
	// The FlipInfo is the one allocation left: the sink's merge writes into it.
	require.LessOrEqual(t, testing.AllocsPerRun(100, func() { f.flipInfo(scene, contents, nil) }), 1.0, "flip report")

	contents[1] = ports.SurfaceContent{ID: 1, Seq: 3}
	second := f.flipInfo(scene, contents, nil)
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, first.Shows, "an earlier snapshot is immutable")
	require.Equal(t, map[domain.WindowID]uint64{1: 3}, second.Shows)
	require.NotSame(t, first, second)
}
