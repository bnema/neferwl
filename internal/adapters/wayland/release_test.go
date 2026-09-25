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
	seen := func(seq uint64) map[ports.WindowID]uint64 { return map[ports.WindowID]uint64{1: seq} }
	for _, tc := range []struct {
		name     string
		h        heldBuffer
		r        ports.OutputPresented
		reported bool
		after    time.Duration
		want     bool
	}{
		{"no report yet", dma, ports.OutputPresented{}, false, 0, true},
		{"output has only this content", dma, ports.OutputPresented{Seen: seen(5)}, true, 0, true},
		{"later content seen", dma, ports.OutputPresented{Seen: seen(6)}, true, 0, false},
		{"scanned out", dma, ports.OutputPresented{Shown: 7, Seen: seen(6)}, true, time.Second, true},
		{"queued for scanout", dma, ports.OutputPresented{Queued: 7, Seen: seen(6)}, true, time.Second, true},
		{"another buffer scanned out", dma, ports.OutputPresented{Shown: 8, Seen: seen(6)}, true, 0, false},
		{"silent output times out", dma, ports.OutputPresented{Seen: seen(5)}, true, heldTimeout, false},
		{"shm follows content", shm, ports.OutputPresented{Shown: 0, Seen: seen(6)}, true, 0, false},
	} {
		if got := keepHeld(tc.h, tc.r, tc.reported, now.Add(tc.after)); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
