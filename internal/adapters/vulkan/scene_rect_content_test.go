package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A window under a presentation-only rect transition shows a rect that differs from its content
// (the client has its final size already). These walk tests need no Vulkan
// device: a single-pixel buffer is a solid quad.
func TestSceneContentVsWindowRect(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	// A draw's rect is its corners: x0, y0, x1, y1.
	// walk returns the content draws: the window's background fill is first.
	walk := func(rect ports.Rect, lw, lh int, geo ports.Rect) []draw {
		c := ports.SurfaceContent{
			ID: 1, Width: 1, Height: 1, LogicalW: lw, LogicalH: lh, Geometry: geo,
			Solid: &ports.SolidColor{R: 1, A: 1}, Opaque: true,
		}
		s := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: rect}}}
		contents := map[ports.WindowID]ports.SurfaceContent{1: c}
		ds := r.draws(s, contents, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
		if len(ds) < 2 {
			t.Fatalf("no content draw: %+v", ds)
		}
		return append([]draw(nil), ds[1:]...)
	}

	t.Run("rect smaller than content is clipped", func(t *testing.T) {
		rect := ports.Rect{X: 10, Y: 10, W: 20, H: 20}
		for _, geo := range []ports.Rect{{}, {X: 5, Y: 5, W: 30, H: 30}} {
			ds := walk(rect, 40, 40, geo)
			clip := image.Rect(10, 10, 30, 30)
			for _, d := range ds {
				got := image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3]))
				if !got.In(clip) {
					t.Fatalf("geometry %+v: draw %v outside rect %v", geo, got, clip)
				}
			}
			if d := ds[0].pc.rect; d != [4]int32{10, 10, 30, 30} {
				t.Fatalf("geometry %+v: content = %v, want the whole rect", geo, d)
			}
		}
	})

	t.Run("rect larger than content shows only the content", func(t *testing.T) {
		rect := ports.Rect{X: 10, Y: 10, W: 50, H: 40}
		ds := walk(rect, 20, 16, ports.Rect{})
		if len(ds) != 1 || ds[0].pc.rect != [4]int32{10, 10, 30, 26} {
			t.Fatalf("content = %+v, want 20x16 at the rect origin", ds)
		}
		// Geometry shifts the surface so its geometry origin sits at the rect origin.
		ds = walk(rect, 24, 20, ports.Rect{X: 4, Y: 4, W: 20, H: 16})
		if len(ds) != 1 || ds[0].pc.rect != [4]int32{10, 10, 30, 26} {
			t.Fatalf("content with geometry = %+v, want 20x16 at the rect origin", ds)
		}
	})
}
