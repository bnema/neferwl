package ports

import "testing"

func TestSceneShowsWorkspaceClip(t *testing.T) {
	s := Scene{OutputWidth: 100, OutputHeight: 100, WorkspaceClip: Rect{X: 25, Y: 25, W: 50, H: 50}, Windows: []SceneWindow{
		{ID: 1, Rect: Rect{W: 20, H: 20}},
		{ID: 2, Rect: Rect{X: 30, Y: 30, W: 20, H: 20}},
		{ID: 3, Rect: Rect{W: 20, H: 20}, Popup: true, OverLayers: true},
	}}
	if s.Shows(1) || !s.Shows(2) || !s.Shows(3) {
		t.Fatalf("shown: %v %v %v", s.Shows(1), s.Shows(2), s.Shows(3))
	}
	s.WorkspaceClip = Rect{}
	if !s.Shows(1) {
		t.Fatal("inherited workspace excluded surface")
	}
}

// A tile under a panel (outside TileClip) is not drawn; a float, a
// fullscreen window, a preview or a popup there still is.
func TestSceneShowsTileClip(t *testing.T) {
	under := Rect{Y: 80, W: 50, H: 80}
	s := Scene{OutputWidth: 100, OutputHeight: 100, TileClip: Rect{W: 100, H: 80}, Windows: []SceneWindow{
		{ID: 1, Rect: Rect{W: 50, H: 80}},
		{ID: 2, Rect: under},
		{ID: 3, Rect: under, Floating: true},
		{ID: 4, Rect: under, Fullscreen: true},
		{ID: 5, Rect: under, Preview: 0.5},
		{ID: 6, Rect: under, Popup: true},
	}}
	for id, want := range map[WindowID]bool{1: true, 2: false, 3: true, 4: true, 5: true, 6: true} {
		if got := s.Shows(id); got != want {
			t.Errorf("window %d shown %v, want %v", id, got, want)
		}
	}
	s.TileClip = Rect{}
	if !s.Shows(2) {
		t.Fatal("tile under no clip not shown")
	}
}
