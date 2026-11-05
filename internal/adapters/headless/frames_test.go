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

// Animating and settled scenes both report the sequence presented.
func TestAnimatingSceneSendsFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes, frames, rec, stop := runWithFrames(t, 4)
		defer stop()

		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Animating: true}
		select {
		case got := <-frames:
			require.Equal(t, domain.OutputFrame{Output: "HEADLESS-1", SceneSeq: 1}, got)
		case <-time.After(3 * time.Second):
			t.Fatal("no frame for an animating scene")
		}

		// The scene an animation settles on is reported too.
		scenes <- domain.Scene{Seq: 2, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rec, 2)
		// Reports retain presentation order across settled scenes.
		scenes <- domain.Scene{Seq: 3, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rec, 3)
		require.Equal(t, domain.OutputFrame{Output: "HEADLESS-1", SceneSeq: 2}, <-frames)
		require.Equal(t, domain.OutputFrame{Output: "HEADLESS-1", SceneSeq: 3}, <-frames)
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

func TestPresentedSceneFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene)
		frames := make(chan domain.OutputFrame, 1)
		contents := newContentFeed(t, 8)
		r, rendered := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Name: "A", Width: 2, Height: 2, Frames: frames, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rendered, 1)
		if got := <-frames; got != (domain.OutputFrame{Output: "A", SceneSeq: 1}) {
			t.Fatal(got)
		}
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		select {
		case got := <-frames:
			t.Fatalf("duplicate: %+v", got)
		default:
		}
		frames <- domain.OutputFrame{Output: "busy"}
		scenes <- domain.Scene{Seq: 2, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rendered, 2)
		// A newer presentation supersedes the pending sequence while still full.
		scenes <- domain.Scene{Seq: 3, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rendered, 3)
		if got := <-frames; got.Output != "busy" {
			t.Fatal(got)
		}
		time.Sleep(time.Second / 60)
		synctest.Wait()
		select {
		case got := <-frames:
			if got.SceneSeq != 3 || got.Output != "A" {
				t.Fatal(got)
			}
		default:
			t.Fatal("no retry of latest presented sequence")
		}
		time.Sleep(time.Second / 60)
		synctest.Wait()
		select {
		case got := <-frames:
			t.Fatalf("duplicate retry: %+v", got)
		default:
		}
		// Animation still wakes core for every presentation, even at seq zero.
		scenes <- domain.Scene{Animating: true, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, rendered, 4)
		select {
		case <-frames:
		default:
			t.Fatal("animation did not wake core")
		}
	})
}
