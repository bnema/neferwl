package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestOrientRoundedWindow(t *testing.T) {
	for transform := domain.BufferTransform(0); transform < 8; transform++ {
		r := &Renderer{width: 120, height: 90}
		sw, sh := r.sceneSize(transform)
		outer := image.Rect(9, 13, 69, 77)
		body := outer.Inset(3)
		s := domain.Scene{Scale: 1, Background: "#ffffff", Border: domain.Border{Width: 3, Active: "#ff8800"},
			Windows:    []domain.SceneWindow{{ID: 1, Rect: domain.Rect{X: 9, Y: 13, W: 60, H: 64}, Inset: domain.SideAll, Radius: 10, Dim: 0.5}},
			Separators: []domain.Separator{{Window: 1, Active: true}}}
		contents := map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Width: 1, Height: 1, LogicalW: 54, LogicalH: 58, Solid: &domain.SolidColor{R: 1, A: 1}}}
		ds := r.draws(s, contents, newDamage(&target{}, s, image.Rect(0, 0, sw, sh)))
		if len(ds) != 4 {
			t.Fatalf("transform %d: %d draws", transform, len(ds))
		}
		// Exercise the non-solid orient path as well as ring/body/dim solids.
		ds[2].pc.misc[0] = modeImage
		radii := ds[0].radii
		r.orient(ds, transform)
		wantOuter := roundRect(transform.RectToBuffer(outer, sw, sh))
		wantBody := roundRect(transform.RectToBuffer(body, sw, sh))
		wantHole := roundRect(transform.RectToBuffer(body.Inset(1), sw, sh))
		if ds[0].round != wantOuter || ds[0].roundInner != wantHole || ds[0].radii != radii {
			t.Fatalf("transform %d: ring clip %+v", transform, ds[0])
		}
		if ds[0].pc.misc[1]&flagRing == 0 {
			t.Fatal("ring must precede content")
		}
		for i, d := range ds {
			want := wantBody
			if i == 0 || i == 3 {
				want = wantOuter
			}
			if d.round != want {
				t.Fatalf("transform %d draw %d clip %v want %v", transform, i, d.round, want)
			}
			rect := [4]float32{float32(d.pc.rect[0]), float32(d.pc.rect[1]), float32(d.pc.rect[2]), float32(d.pc.rect[3])}
			if rect != want {
				t.Fatalf("transform %d draw %d rect %v want %v", transform, i, rect, want)
			}
		}
	}
}
