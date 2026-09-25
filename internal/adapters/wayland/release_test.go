package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

func TestKeepHeld(t *testing.T) {
	now := time.Unix(100, 0)
	dma := heldBuffer{window: 1, id: 7, after: 5, at: now}
	shm := heldBuffer{window: 1, after: 5, at: now}
	out := func(shown, queued, seq uint64) ports.OutputPresented {
		return ports.OutputPresented{Shown: shown, Queued: queued, Seen: map[ports.WindowID]uint64{1: seq}}
	}
	type R = []ports.OutputPresented
	for _, tc := range []struct {
		name    string
		h       heldBuffer
		reports R
		after   time.Duration
		want    bool
	}{
		{"no report yet", dma, nil, 0, true},
		{"no report times out", dma, nil, heldTimeout, false},
		{"output has only this content", dma, R{out(0, 0, 5)}, 0, true},
		{"later content seen", dma, R{out(0, 0, 6)}, 0, false},
		{"scanned out", dma, R{out(7, 0, 6)}, time.Second, true},
		{"queued for scanout", dma, R{out(0, 7, 6)}, time.Second, true},
		{"another buffer scanned out", dma, R{out(8, 0, 6)}, 0, false},
		{"silent output times out", dma, R{out(0, 0, 5)}, heldTimeout, false},
		{"shm follows content", shm, R{out(0, 0, 6)}, 0, false},
		// A window moved off output A: A still scans the buffer out.
		{"other output scans it out", dma, R{out(0, 0, 6), out(7, 0, 6)}, time.Second, true},
		{"other output lags", dma, R{out(0, 0, 6), out(0, 0, 5)}, 0, true},
		{"all outputs moved on", dma, R{out(0, 0, 6), out(0, 0, 6)}, 0, false},
	} {
		if got := keepHeld(tc.h, tc.reports, now.Add(tc.after)); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
