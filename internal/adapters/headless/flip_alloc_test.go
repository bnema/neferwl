package headless

import (
	"github.com/bnema/neferwl/internal/adapters/outputkit/presented"
	"github.com/bnema/neferwl/internal/ports"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

// Steady-state flip reporting: the Shows snapshot is reused while it does not
// change, and a changed one never mutates what an earlier flip handed out.
func TestFlipShowsAllocations(t *testing.T) {
	var f flipShows
	scene := domain.Scene{OutputWidth: 10, OutputHeight: 10, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 10, H: 10}}}}
	contents := map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Seq: 1}, 2: {ID: 2, Seq: 2}}
	var q presented.Queue
	flip := f.flipInfo(scene, contents, nil)
	q.Push(ports.OutputPresented{Flip: &flip})
	first := q.Unsent()[0].Flip
	defer q.Reset()
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, first.Shows)

	require.Zero(t, testing.AllocsPerRun(100, func() { f.of(scene, contents, nil) }), "unchanged shows")
	// The value is copied into the report lease without allocation.
	require.LessOrEqual(t, testing.AllocsPerRun(100, func() { f.flipInfo(scene, contents, nil) }), 0.0, "flip report")

	contents[1] = domain.SurfaceContent{ID: 1, Seq: 3}
	second := f.flipInfo(scene, contents, nil)
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, first.Shows, "an earlier snapshot is immutable")
	require.Equal(t, map[domain.WindowID]uint64{1: 3}, second.Shows)
	require.NotSame(t, first, &second)
}
