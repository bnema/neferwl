package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestDropPreviewBlocksPlanes(t *testing.T) {
	s, c := overlayScene()
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 10, W: 4, H: 100}}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "drop_preview" {
		t.Fatalf("overlay reason %q", reason)
	}
	fs, fc := fullscreenScene()
	if _, reason := scanoutCandidate(fs, fc, 200, 100, nil); reason == "drop_preview" {
		t.Fatal("no preview, still refused")
	}
	fs.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 10, W: 4, H: 100}}
	if _, reason := scanoutCandidate(fs, fc, 200, 100, nil); reason != "drop_preview" {
		t.Fatalf("scanout reason %q", reason)
	}
}
