package vulkan

import (
	"image"
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A window's Fade scales its body fill, its own border lines, its dim and
// its content by 1-Fade (premultiplied); at 1 the window draws nothing. The
// walk needs no Vulkan device: a solid single-pixel buffer imports nothing.
func TestSceneWindowFade(t *testing.T) {
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-6 }
	walk := func(r *Renderer, fade float64) []draw {
		s := ports.Scene{
			Background: "#ffffff", Dim: 0, Border: ports.Border{Width: 1, Active: "#ffffff", Inactive: "#808080"},
			Windows: []ports.SceneWindow{
				{ID: 1, Rect: ports.Rect{W: 40, H: 60}},
				{ID: 2, Floating: true, Rect: ports.Rect{X: 40, W: 20, H: 20}, Inset: ports.SideLeft | ports.SideRight | ports.SideTop | ports.SideBottom, Fade: fade, Dim: 0.5},
			},
			Separators: []ports.Separator{{Rect: ports.Rect{X: 40, W: 1, H: 20}, Window: 2}},
		}
		contents := map[ports.WindowID]ports.SurfaceContent{
			2: {ID: 2, Width: 1, Height: 1, LogicalW: 18, LogicalH: 18, Solid: &ports.SolidColor{R: 1, G: 0.5, B: 0, A: 1}},
		}
		// The renderer reuses its draw buffer: each walk's result is kept.
		return slices.Clone(r.draws(s, contents, newDamage(&target{}, s, image.Rect(0, 0, 80, 60))))
	}
	r := &Renderer{width: 80, height: 60}
	opaque := walk(r, 0)
	// Tile fill, float body fill, float content, float line, float dim.
	if len(opaque) != 5 {
		t.Fatalf("draws = %d, want 5: %+v", len(opaque), opaque)
	}
	half := walk(r, 0.5)
	if len(half) != 5 {
		t.Fatalf("faded draws = %d, want 5", len(half))
	}
	if half[0].pc.color != opaque[0].pc.color {
		t.Fatalf("the tile's fill changed with the float's fade: %v", half[0].pc.color)
	}
	for i := 1; i < 5; i++ {
		for ch := range 4 {
			if !near(half[i].pc.color[ch], opaque[i].pc.color[ch]*0.5) {
				t.Fatalf("draw %d channel %d: %v, want half of %v", i, ch, half[i].pc.color, opaque[i].pc.color)
			}
		}
		if half[i].pc.rect != opaque[i].pc.rect {
			t.Fatalf("draw %d moved with the fade: %v vs %v", i, half[i].pc.rect, opaque[i].pc.rect)
		}
	}
	if got := half[2].pc.misc[0]; got != modeSolid {
		t.Fatalf("content draw mode %d", got)
	}
	// Faded out: nothing of the float, the tile unchanged.
	gone := walk(r, 1)
	if len(gone) != 1 || gone[0].pc.color != opaque[0].pc.color {
		t.Fatalf("faded-out draws = %+v, want the tile only", gone)
	}
	// A second window after the faded one draws at full alpha.
	s := ports.Scene{Background: "#ffffff", Windows: []ports.SceneWindow{
		{ID: 1, Rect: ports.Rect{W: 40, H: 60}, Fade: 0.25},
		{ID: 2, Rect: ports.Rect{X: 40, W: 40, H: 60}},
	}}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	if len(ds) != 2 || !near(ds[0].pc.color[3], 0.75) || ds[1].pc.color[3] != 1 {
		t.Fatalf("alpha leaked past the faded window: %+v", ds)
	}
}

// On an HDR output a solid's rgb is re-encoded so the shader's decode
// yields the faded linear premultiplied color.
func TestSceneWindowFadeHDR(t *testing.T) {
	r := &Renderer{width: 80, height: 60, hdrNits: 203}
	s := ports.Scene{Background: "#ffffff", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 40, H: 60}, Fade: 0.5}}}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	if len(ds) != 1 {
		t.Fatalf("draws = %d", len(ds))
	}
	want := float32(linearToSRGB(0.5))
	c := ds[0].pc.color
	if math.Abs(float64(c[0]-want)) > 1e-6 || c[0] != c[1] || c[1] != c[2] || c[3] != 0.5 {
		t.Fatalf("hdr faded white = %v, want rgb %v alpha 0.5", c, want)
	}
}
