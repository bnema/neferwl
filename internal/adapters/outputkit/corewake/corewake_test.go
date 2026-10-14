package corewake

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

// Send never blocks: a frame core has not read is enough.
func TestSend(t *testing.T) {
	ch := make(chan ports.OutputFrame, 1)
	Send(ch, "DP-1")
	Send(ch, "DP-1") // full: dropped, not blocked
	require.Equal(t, []ports.OutputFrame{{Output: "DP-1"}}, []ports.OutputFrame{<-ch})
	require.Empty(t, ch)
	require.NotPanics(t, func() { Send(nil, "DP-1") })
}

func TestSendAllocations(t *testing.T) {
	ch := make(chan ports.OutputFrame, 1)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		Send(ch, "DP-1")
		select {
		case <-ch:
		default:
		}
		Send(ch, "DP-1")
	}))
}
