package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// A flip of an animating scene tells core the next frame is due; the flip
// of any other frame does not.
func TestCompletedSendsFrameOnlyForAnimatingScene(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	frames := make(chan ports.OutputFrame, 2)
	o.Frames = frames
	seen := map[ports.WindowID]uint64{}

	require.NoError(t, o.commitFrame(70, nil, false, false, pendingFrame{}))
	require.True(t, o.completed(eventOf((*commits)[0]), seen))
	require.Empty(t, frames, "a settled scene wakes nobody")

	require.NoError(t, o.commitFrame(70, nil, false, false, pendingFrame{animating: true}))
	require.True(t, o.completed(eventOf((*commits)[1]), seen))
	require.Equal(t, []ports.OutputFrame{{Output: "DP-1"}}, drain(frames))

}

// A frames channel core has not read stays full: the owner never waits.
func TestCompletedFramesNeverBlock(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	frames := make(chan ports.OutputFrame, 1)
	o.Frames = frames
	seen := map[ports.WindowID]uint64{}
	for i := range 3 {
		require.NoError(t, o.commitFrame(70, nil, false, false, pendingFrame{animating: true}))
		require.True(t, o.completed(eventOf((*commits)[i]), seen))
	}
	require.Len(t, frames, 1)
}

// An output without a frames channel (tests, nobody to wake) just flips.
func TestCompletedWithoutFramesChannel(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	require.NoError(t, o.commitFrame(70, nil, false, false, pendingFrame{animating: true}))
	require.True(t, o.completed(eventOf((*commits)[0]), map[ports.WindowID]uint64{}))
}

func drain(ch chan ports.OutputFrame) []ports.OutputFrame {
	var out []ports.OutputFrame
	for len(ch) > 0 {
		out = append(out, <-ch)
	}
	return out
}

// The frame send of an animating flip allocates nothing, full channel or not.
func TestAnimatingFrameSendAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	frames := make(chan ports.OutputFrame, 1)
	o.Frames = frames
	if n := testing.AllocsPerRun(100, func() {
		o.wakeCore() // fills the channel, or finds it full
		select {
		case <-frames:
		default:
		}
		o.wakeCore()
	}); n != 0 {
		t.Fatalf("wakeCore: %v allocations", n)
	}
}
