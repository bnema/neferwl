package wayland

import (
	"testing"
	"time"
)

func TestKeepHeld(t *testing.T) {
	now := time.Unix(100, 0)
	h := heldBuffer{id: 7, at: now}
	for _, tc := range []struct {
		name                string
		scanned             uint64
		live, flipped, want bool
		after               time.Duration
	}{
		{"waits for a flip", 0, true, false, true, 0},
		{"composed flip releases", 0, true, true, false, 0},
		{"scanned out stays", 7, true, true, true, time.Second},
		{"another buffer scanned out", 8, true, true, false, 0},
		{"idle output times out", 0, true, false, false, heldTimeout},
		{"unplugged output releases", 0, false, false, false, 0},
	} {
		if got := keepHeld(h, tc.scanned, tc.live, tc.flipped, now.Add(tc.after)); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
