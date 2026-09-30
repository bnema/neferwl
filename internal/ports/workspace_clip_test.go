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
