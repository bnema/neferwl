package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// A flip of an animating scene tells core the next frame is due; the flip of
// any other frame does not. A frames channel core has not read stays full: the
// owner never waits. An output without a frames channel (tests, nobody to wake)
// just flips.
func TestCompletedFrames(t *testing.T) {
	tests := []struct {
		name      string
		buffer    int    // frames channel capacity; 0: no channel
		animating []bool // the flips, one per frame
		want      []ports.OutputFrame
	}{
		{name: "a settled scene wakes nobody", buffer: 2, animating: []bool{false}},
		{name: "an animating scene wakes core once", buffer: 2, animating: []bool{false, true}, want: []ports.OutputFrame{{Output: "DP-1"}}},
		{name: "a full channel never blocks", buffer: 1, animating: []bool{true, true, true}, want: []ports.OutputFrame{{Output: "DP-1"}}},
		{name: "no frames channel", animating: []bool{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, commits := testOutput(t)
			o.cursor = nil
			var frames chan ports.OutputFrame
			if tt.buffer > 0 {
				frames = make(chan ports.OutputFrame, tt.buffer)
				o.Frames = frames
			}
			seen := map[ports.WindowID]uint64{}
			for i, animating := range tt.animating {
				require.NoError(t, o.commitFrame(70, nil, false, false, pendingFrame{animating: animating}))
				require.True(t, o.completed(eventOf((*commits)[i]), seen))
			}
			if frames != nil {
				require.Equal(t, tt.want, drain(frames))
			}
		})
	}
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
