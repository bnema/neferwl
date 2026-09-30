package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestSceneBelowFloatBoundary(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := ports.Scene{Border: ports.Border{Active: "#ffffff"}, Windows: []ports.SceneWindow{
		{ID: 1, Floating: true, Below: true, Rect: ports.Rect{W: 80, H: 60}},
		{ID: 2, Rect: ports.Rect{W: 40, H: 60}},
	}, Separators: []ports.Separator{{Rect: ports.Rect{W: 80, H: 1}, Active: true}}}
	draws := func() []draw { return r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60))) }
	ds := draws()
	if len(ds) != 3 || ds[0].pc.rect[2] != 80 || ds[1].pc.rect[2] != 40 || ds[2].pc.rect[3] != 1 {
		t.Fatalf("below float / tile / lines: %+v", ds)
	}
	s.Dim = 0.4
	s.Windows = append(s.Windows, ports.SceneWindow{ID: 3, Floating: true, Rect: ports.Rect{X: 50, W: 20, H: 20}})
	ds = draws()
	if len(ds) != 5 || ds[3].pc.color != [4]float32{0, 0, 0, 0.4} || ds[4].pc.rect[0] != 50 {
		t.Fatalf("veil boundary: %+v", ds)
	}
	// An overview preview of a float is not a float above the tiles: the
	// tile lines follow the last window.
	s.Dim = 0
	s.Windows = []ports.SceneWindow{
		{ID: 1, Floating: true, Preview: 0.5, Rect: ports.Rect{W: 40, H: 30}},
		{ID: 2, Preview: 0.5, Rect: ports.Rect{X: 40, W: 40, H: 30}},
	}
	ds = draws()
	if len(ds) != 3 || ds[2].pc.rect[3] != 1 {
		t.Fatalf("preview boundary: %+v", ds)
	}
}
