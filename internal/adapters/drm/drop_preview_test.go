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

// A Proxy thumbnail only comes with a preview: neither plane path takes
// the frame, whichever window the proxy stands for.
func TestDropProxyNeverOnAPlane(t *testing.T) {
	fs, fc := fullscreenScene()
	fs.Windows = append(fs.Windows, domain.SceneWindow{ID: 1, Proxy: true, Rect: domain.Rect{X: 10, Y: 10, W: 50, H: 25}, Zoom: 0.25})
	fs.DropPreview = &domain.DropPreview{Rect: domain.Rect{W: 4, H: 100}, Thumb: 1, ThumbRect: domain.Rect{X: 10, Y: 10, W: 50, H: 25}}
	if _, reason := scanoutCandidate(fs, fc, 200, 100, nil); reason == "" {
		t.Fatal("scanned out with a proxy")
	}
	if _, _, _, reason := overlayCandidate(fs, fc, false, nil, nil); reason == "" {
		t.Fatal("overlay with a proxy")
	}
}
