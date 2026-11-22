package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestSceneDropPreviewOrder(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := domain.Scene{
		Border: domain.Border{Active: "#ff0000"},
		Windows: []domain.SceneWindow{
			{ID: 1, Rect: domain.Rect{W: 40, H: 60}},
			{ID: 2, Floating: true, Rect: domain.Rect{X: 50, W: 20, H: 20}},
		},
		Layers:      []domain.SceneLayer{{ID: 9, Layer: domain.LayerTop, Rect: domain.Rect{W: 80, H: 5}}},
		DropPreview: &domain.DropPreview{Rect: domain.Rect{X: 38, W: 4, H: 60}},
	}
	got := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	// tile, float, the zone, then the top layer.
	zone := -1
	for i, d := range got {
		if d.pc.color == [4]float32{1, 0, 0, 1} {
			if zone >= 0 {
				t.Fatalf("two zones: %+v", got)
			}
			zone = i
		}
	}
	if zone != 2 || got[zone].pc.rect != [4]int32{38, 0, 42, 60} {
		t.Fatalf("zone at %d: %+v", zone, got)
	}
	s.DropPreview = nil
	if n := len(r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))); n != len(got)-1 {
		t.Fatalf("draws without preview = %d, want %d", n, len(got)-1)
	}
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 38, H: 60}}
	if n := len(r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))); n != len(got)-1 {
		t.Fatalf("draws with an empty zone = %d, want %d", n, len(got)-1)
	}
}
