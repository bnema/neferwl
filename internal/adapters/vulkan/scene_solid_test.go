package vulkan

import (
	"image"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// These walk tests need no Vulkan device: a single-pixel buffer is a solid
// quad, which imports nothing.
func TestSceneSolid(t *testing.T) {
	walk := func(r *Renderer, c ports.SurfaceContent) []draw {
		c.ID = 1
		c.Width, c.Height, c.LogicalW, c.LogicalH = 1, 1, 20, 20
		s := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 20}}}}
		contents := map[ports.WindowID]ports.SurfaceContent{1: c}
		return r.draws(s, contents, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	}
	// The window's own background fill comes first; the content follows.
	solids := func(ds []draw) []draw {
		if len(ds) == 0 || ds[0].pc.misc[0] != modeSolid || ds[0].pc.rect != [4]int32{0, 0, 20, 20} {
			t.Fatalf("no window background first: %+v", ds)
		}
		return ds[1:]
	}
	near := func(a, b [4]float32) bool {
		for i := range a {
			if math.Abs(float64(a[i]-b[i])) > 1e-6 {
				return false
			}
		}
		return true
	}
	half := &ports.SolidColor{R: 0.25, G: 0.125, B: 0, A: 0.5} // premultiplied
	t.Run("premultiplied", func(t *testing.T) {
		r := &Renderer{width: 80, height: 60}
		got := solids(walk(r, ports.SurfaceContent{Solid: half}))
		if len(got) != 1 || got[0].pc.color != [4]float32{0.25, 0.125, 0, 0.5} {
			t.Fatalf("draws = %+v", got)
		}
	})
	t.Run("fade", func(t *testing.T) {
		r := &Renderer{width: 80, height: 60}
		got := solids(walk(r, ports.SurfaceContent{Solid: half, Fade: 0.5}))
		if len(got) != 1 || got[0].pc.color != [4]float32{0.125, 0.0625, 0, 0.25} {
			t.Fatalf("draws = %+v", got)
		}
	})
	t.Run("opaque", func(t *testing.T) {
		r := &Renderer{width: 80, height: 60}
		got := solids(walk(r, ports.SurfaceContent{Solid: half, Opaque: true}))
		if len(got) != 1 || got[0].pc.color != [4]float32{0.25, 0.125, 0, 1} {
			t.Fatalf("draws = %+v", got)
		}
	})
	t.Run("hdr", func(t *testing.T) {
		r := &Renderer{width: 80, height: 60, hdrNits: 203}
		got := solids(walk(r, ports.SurfaceContent{Solid: half}))
		if len(got) != 1 {
			t.Fatalf("draws = %+v", got)
		}
		// The HDR shader decodes rgb (decodeSRGB) and blends linear light:
		// decoded values must be the linear straight color times alpha.
		c := got[0].pc.color
		decoded := [4]float32{float32(srgbToLinear(float64(c[0]))), float32(srgbToLinear(float64(c[1]))), float32(srgbToLinear(float64(c[2]))), c[3]}
		want := [4]float32{float32(srgbToLinear(0.5) * 0.5), float32(srgbToLinear(0.25) * 0.5), 0, 0.5}
		if !near(decoded, want) {
			t.Fatalf("decoded = %v, want %v", decoded, want)
		}
	})
	t.Run("transparent", func(t *testing.T) {
		r := &Renderer{width: 80, height: 60}
		if got := solids(walk(r, ports.SurfaceContent{Solid: &ports.SolidColor{}})); len(got) != 0 {
			t.Fatalf("draws = %+v", got)
		}
		// Fully faded away too.
		if got := walk(r, ports.SurfaceContent{Solid: half, Fade: 1}); len(solids(got)) != 0 {
			t.Fatalf("faded draws = %+v", got)
		}
	})
}

func TestLinearToSRGB(t *testing.T) {
	for _, v := range []float64{0, 0.002, 0.04045, 0.5, 1} {
		if got := srgbToLinear(linearToSRGB(v)); math.Abs(got-v) > 1e-6 {
			t.Errorf("round trip of %v = %v", v, got)
		}
		if got := linearToSRGB(srgbToLinear(v)); math.Abs(got-v) > 1e-6 {
			t.Errorf("inverse round trip of %v = %v", v, got)
		}
	}
}
