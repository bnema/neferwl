package app

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

// Recording unchanged shot sizes, the per-frame path, allocates nothing.
func TestRecordShotSizesSteadyState(t *testing.T) {
	var s session
	scenes := []domain.Scene{{Output: "A", OutputWidth: 640, OutputHeight: 480, Scale: 1.5}}
	s.recordShotSizes(scenes)
	if w, h, ok := s.shotSize("A"); !ok || w != 960 || h != 720 {
		t.Fatalf("size %dx%d %v", w, h, ok)
	}
	if n := testing.AllocsPerRun(100, func() { s.recordShotSizes(scenes) }); n != 0 {
		t.Fatalf("steady state: %v allocations", n)
	}
	if _, _, ok := s.shotSize("B"); ok {
		t.Fatal("unknown output has a size")
	}
}
