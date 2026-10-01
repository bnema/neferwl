package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestSceneDropHintsOrder(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := ports.Scene{
		Border: ports.Border{Active: "#ff0000"},
		Windows: []ports.SceneWindow{
			{ID: 1, Rect: ports.Rect{W: 40, H: 60}},
			{ID: 2, Floating: true, Rect: ports.Rect{X: 50, W: 20, H: 20}},
		},
		Layers:    []ports.SceneLayer{{ID: 9, Layer: ports.LayerTop, Rect: ports.Rect{W: 80, H: 5}}},
		DropHints: []ports.Rect{{X: 38, W: 4, H: 60}, {W: 0, H: 10}},
	}
	got := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	// tile, float, one hint (the empty one is skipped), then the top layer.
	hint := -1
	for i, d := range got {
		if d.pc.color == [4]float32{1, 0, 0, 1} {
			if hint >= 0 {
				t.Fatalf("two hints: %+v", got)
			}
			hint = i
		}
	}
	if hint != 2 || got[hint].pc.rect != [4]int32{38, 0, 42, 60} {
		t.Fatalf("hint at %d: %+v", hint, got)
	}
	s.DropHints = nil
	if n := len(r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))); n != len(got)-1 {
		t.Fatalf("draws without hints = %d, want %d", n, len(got)-1)
	}
}
