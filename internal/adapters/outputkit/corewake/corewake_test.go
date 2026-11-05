package corewake

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

// Send never blocks: a frame core has not read is enough.
func TestSend(t *testing.T) {
	ch := make(chan domain.OutputFrame, 1)
	Send(ch, "DP-1", 7)
	Send(ch, "DP-1", 7) // full: dropped, not blocked
	require.Equal(t, []domain.OutputFrame{{Output: "DP-1", SceneSeq: 7}}, []domain.OutputFrame{<-ch})
	require.Empty(t, ch)
	require.NotPanics(t, func() { Send(nil, "DP-1", 7) })
}

func TestSendAllocations(t *testing.T) {
	ch := make(chan domain.OutputFrame, 1)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		Send(ch, "DP-1", 7)
		select {
		case <-ch:
		default:
		}
		Send(ch, "DP-1", 7)
	}))
}
