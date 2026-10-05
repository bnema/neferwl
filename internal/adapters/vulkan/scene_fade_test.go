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

// An appearing or leaving float carries a Zoom, not a Preview: it still
// opens the floats, so the tile lines and the veil are painted under it,
// exactly as for the same float unzoomed; only its content draw shrinks.
func TestSceneZoomedFloatOpensFloats(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	scene := func(zoom float64) ports.Scene {
		return ports.Scene{
			Dim: 0.4, Background: "#000000", Border: ports.Border{Width: 1, Active: "#ffffff", Inactive: "#808080"},
			Windows: []ports.SceneWindow{
				{ID: 1, Rect: ports.Rect{W: 80, H: 60}},
				{ID: 2, Floating: true, Rect: ports.Rect{X: 20, Y: 10, W: 40, H: 40}, Inset: ports.SideAll, Fade: 0.5, Zoom: zoom},
			},
			Separators: []ports.Separator{
				{Rect: ports.Rect{X: 40, W: 1, H: 60}},
				{Rect: ports.Rect{X: 20, Y: 10, W: 1, H: 40}, Window: 2},
			},
		}
	}
	contents := map[ports.WindowID]ports.SurfaceContent{
		2: {ID: 2, Width: 1, Height: 1, LogicalW: 38, LogicalH: 38, Solid: &ports.SolidColor{R: 1, G: 1, B: 1, A: 1}},
	}
	walk := func(s ports.Scene) []draw {
		return slices.Clone(r.draws(s, contents, newDamage(&target{}, s, image.Rect(0, 0, 80, 60))))
	}
	plain, zoomed := walk(scene(0)), walk(scene(0.9))
	// tile fill, tile line, veil, float fill, float content, float line
	if len(plain) != 6 || len(zoomed) != 6 {
		t.Fatalf("draws: plain %d zoomed %d, want 6", len(plain), len(zoomed))
	}
	for i := range plain {
		if i == 4 {
			continue
		}
		if plain[i].pc != zoomed[i].pc {
			t.Fatalf("draw %d differs with the zoom: %+v vs %+v", i, plain[i].pc, zoomed[i].pc)
		}
	}
	if veil := zoomed[2].pc; veil.rect != [4]int32{0, 0, 80, 60} || veil.color != [4]float32{0, 0, 0, 0.4} {
		t.Fatalf("veil = %+v, want the full-output 0.4 dim under the float", veil)
	}
	if zoomed[1].pc.rect != [4]int32{40, 0, 41, 60} {
		t.Fatalf("tile line = %+v, want it painted before the float", zoomed[1].pc.rect)
	}
	pr, zr := plain[4].pc.rect, zoomed[4].pc.rect
	if !(zr[2]-zr[0] < pr[2]-pr[0]) || zr[0] != pr[0] || zr[1] != pr[1] {
		t.Fatalf("zoomed content %v, want smaller than %v from the same origin", zr, pr)
	}
	// A card (Preview) keeps not opening the floats: no tile lines, no veil.
	card := scene(0)
	card.Windows[1].Preview = 0.9
	if ds := walk(card); len(ds) != 5 || ds[1].pc.color[3] != 0.5 {
		t.Fatalf("card draws = %d, want 5 (lines after, no veil): %+v", len(ds), ds)
	}
}
