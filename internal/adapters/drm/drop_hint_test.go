package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestDropHintsBlockPlanes(t *testing.T) {
	s, c := overlayScene()
	s.DropHints = []ports.Rect{{X: 10, W: 4, H: 100}}
	if _, _, _, reason := overlayCandidate(s, c, false, nil); reason != "drop_hint" {
		t.Fatalf("overlay reason %q", reason)
	}
	fs, fc := fullscreenScene()
	if _, reason := scanoutCandidate(fs, fc, 200, 100); reason == "drop_hint" {
		t.Fatal("no hints, still refused")
	}
	fs.DropHints = []ports.Rect{{X: 10, W: 4, H: 100}}
	if _, reason := scanoutCandidate(fs, fc, 200, 100); reason != "drop_hint" {
		t.Fatalf("scanout reason %q", reason)
	}
}
