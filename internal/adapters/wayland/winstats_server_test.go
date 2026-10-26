package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestWindowStatsRecording(t *testing.T) {
	var w windowStats
	c := ports.SurfaceContent{Width: 640, Height: 480}
	now := time.Unix(1, 0)
	w.commit(now, c, false, false)
	w.flip(&ports.FlipInfo{Shows: map[ports.WindowID]uint64{1: 1}})
	if w.client.n != 0 || w.shown.n != 0 || w.facts != (ports.WindowStats{}) {
		t.Fatal("unwatched recording")
	}
	w.watched = 1
	for i := range 4 {
		w.commit(now.Add(time.Duration(i)*20*time.Millisecond), c, false, true)
	}
	if w.client.fps() != 50 || w.facts.Width != 640 || w.facts.Height != 480 || w.facts.DMABuf || !w.facts.Tearing {
		t.Fatalf("commit stats: %+v", w.facts)
	}
	for i := range 3 {
		w.flip(&ports.FlipInfo{When: time.Duration(i) * 10 * time.Millisecond, Shows: map[ports.WindowID]uint64{1: uint64(i + 1)}, ZeroCopy: 1, HardwareClock: true})
	}
	w.flip(&ports.FlipInfo{When: time.Second, Shows: map[ports.WindowID]uint64{1: 3}, ScanoutReason: "composition", OverlayReason: "busy"})
	if w.shown.n != 2 || w.shown.fps() != 100 {
		t.Fatalf("shown samples %d FPS %v", w.shown.n, w.shown.fps())
	}
	if w.facts.ScanoutReason != "composition" || w.facts.OverlayReason != "busy" {
		t.Fatal("missing reasons")
	}
	w.flip(&ports.FlipInfo{Shows: map[ports.WindowID]uint64{1: 3}, ZeroCopy: 1, HardwareClock: true})
	if !w.facts.Scanout || !w.facts.VRR {
		t.Fatal("missing scanout/VRR")
	}
}
