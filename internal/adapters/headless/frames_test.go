package headless

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// runWithFrames runs an output with a frames channel of the given size and
// returns what it needs to feed scenes and stop it.
func runWithFrames(t *testing.T, size int) (scenes chan domain.Scene, frames chan domain.OutputFrame, f *frames, stop func()) {
	t.Helper()
	scenes = make(chan domain.Scene, 8)
	frames = make(chan domain.OutputFrame, size)
	r, rec := recordingRenderer(t, nil)
	sink, _ := presentedSink(t, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunOutput(ctx, Options{Width: 2, Height: 2, Name: "HEADLESS-1", Presented: sink, Frames: frames,
			NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
	}()
	return scenes, frames, rec, func() {
		cancel()
		require.NoError(t, <-done)
	}
}

// The presentation of an animating scene tells core the next frame is due;
// the one of a settled scene does not.
func TestAnimatingSceneSendsFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes, frames, rec, stop := runWithFrames(t, 4)
		defer stop()

		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Animating: true}
		select {
		case got := <-frames:
			require.Equal(t, domain.OutputFrame{Output: "HEADLESS-1"}, got)
		case <-time.After(3 * time.Second):
			t.Fatal("no frame for an animating scene")
		}

		// The scene an animation settles on: drawn, nobody woken.
		scenes <- domain.Scene{Seq: 2, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rec, 2)
		// A third scene is drawn after the settled one (the owner handles scenes
		// in order): had the settled frame woken core, its frame precedes the
		// third's in the channel.
		scenes <- domain.Scene{Seq: 3, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rec, 3)
		require.Empty(t, frames)
	})
}

// A frames channel core has not read stays full: the owner never waits.
func TestAnimatingFramesNeverBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes, frames, rec, stop := runWithFrames(t, 1)
		defer stop()
		for seq := uint64(1); seq <= 3; seq++ {
			scenes <- domain.Scene{Seq: seq, OutputWidth: 2, OutputHeight: 2, Animating: true}
			waitFrames(t, rec, int(seq))
		}
		require.Len(t, frames, 1)
	})
}
