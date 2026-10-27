package ports

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestSceneShowsWorkspaceClip(t *testing.T) {
	s := Scene{OutputWidth: 100, OutputHeight: 100, WorkspaceClip: domain.Rect{X: 25, Y: 25, W: 50, H: 50}, Windows: []SceneWindow{
		{ID: 1, Rect: domain.Rect{W: 20, H: 20}},
		{ID: 2, Rect: domain.Rect{X: 30, Y: 30, W: 20, H: 20}},
		{ID: 3, Rect: domain.Rect{W: 20, H: 20}, Popup: true, OverLayers: true},
	}}
	if s.Shows(1) || !s.Shows(2) || !s.Shows(3) {
		t.Fatalf("shown: %v %v %v", s.Shows(1), s.Shows(2), s.Shows(3))
	}
	s.WorkspaceClip = domain.Rect{}
	if !s.Shows(1) {
		t.Fatal("inherited workspace excluded surface")
	}
}

// A tile under a panel (outside TileArea) is not drawn; a float, a
// fullscreen window, a preview or a popup there still is. Panels covering
// the whole output leave no tile drawn.
func TestSceneShowsTileInset(t *testing.T) {
	under := domain.Rect{Y: 80, W: 50, H: 80}
	s := Scene{OutputWidth: 100, OutputHeight: 100, TileInset: domain.Insets{Bottom: 20}, Windows: []SceneWindow{
		{ID: 1, Rect: domain.Rect{W: 50, H: 80}},
		{ID: 2, Rect: under},
		{ID: 3, Rect: under, Floating: true},
		{ID: 4, Rect: under, Fullscreen: true},
		{ID: 5, Rect: under, Preview: 0.5},
		{ID: 6, Rect: under, Popup: true},
	}}
	for id, want := range map[domain.WindowID]bool{1: true, 2: false, 3: true, 4: true, 5: true, 6: true} {
		if got := s.Shows(id); got != want {
			t.Errorf("window %d shown %v, want %v", id, got, want)
		}
	}
	s.TileInset = domain.Insets{Right: 100, Bottom: 100}
	if s.Shows(1) || !s.Shows(3) {
		t.Fatalf("no room for tiles: tile %v, float %v", s.Shows(1), s.Shows(3))
	}
	s.TileInset = domain.Insets{}
	if !s.Shows(2) {
		t.Fatal("tile with no panel not shown")
	}
}
