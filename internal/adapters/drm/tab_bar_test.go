package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestTabBarBlocksPlanes(t *testing.T) {
	bars := []domain.TabBar{{Rect: domain.Rect{W: 100, H: 20}}}
	s, c := overlayScene()
	s.TabBars = bars
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "tab_bar" {
		t.Fatalf("overlay reason %q", reason)
	}
	fs, fc := fullscreenScene()
	if _, reason := scanoutCandidate(fs, fc, 200, 100, nil); reason == "tab_bar" {
		t.Fatal("no bar, still refused")
	}
	fs.TabBars = bars
	if _, reason := scanoutCandidate(fs, fc, 200, 100, nil); reason != "tab_bar" {
		t.Fatalf("scanout reason %q", reason)
	}
}
