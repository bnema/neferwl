package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/motion"
)

// A workspace slide snaps in output pixels: at a 600 px output half a pixel
// is 1/1200 of a workspace.
func TestSwitchSpringSnapsAtOutputPixels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitorWithIDs("", "", new(uint64))
		m.template.Output = Rect{W: 800, H: 600}
		s := m.switchSpring(0.5, 0)
		if s.Snap != motion.PixelSnap/600 {
			t.Fatalf("snap %v", s.Snap)
		}
		for _, tc := range []struct {
			from float64
			done bool
		}{{0.5 / 600, false}, {0.2 / 600, true}} {
			s.From = tc.from
			start := time.Unix(10, 0)
			sample := motion.NewMotion(s, start, 1)
			if _, done := sample.At(start); done != tc.done {
				t.Fatalf("snap threshold at %v: settled %t, want %t", tc.from, done, tc.done)
			}
		}

	})
}
