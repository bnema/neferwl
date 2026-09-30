package capture

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// A content change of a window only the hidden workspace draws needs a frame
// (its child render and the reads it releases); the displayed scene alone
// would say no.
func TestShowsCountsHiddenWorkspace(t *testing.T) {
	s := ports.Scene{
		OutputWidth: 100, OutputHeight: 50,
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 100, H: 50}}},
		CaptureScene: &ports.Scene{
			OutputWidth: 100, OutputHeight: 50,
			Windows: []ports.SceneWindow{{ID: 5, Rect: ports.Rect{W: 100, H: 50}}},
		},
	}
	require.True(t, Shows(s, 1), "displayed window")
	require.True(t, Shows(s, 5), "hidden workspace window")
	require.False(t, s.Shows(5), "the displayed scene alone does not show it")
	require.False(t, Shows(s, 9), "unrelated window")
	s.CaptureScene = nil
	require.False(t, Shows(s, 5), "no hidden workspace, no frame")
	require.True(t, Shows(s, 1))
}
