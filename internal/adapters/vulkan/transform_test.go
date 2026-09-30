package vulkan

import (
	"image/color"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// Every wl_output.transform draws the buffer through its inverse: each
// surface pixel shows the buffer pixel ToBuffer names, pixel exact at 1:1,
// with and without a viewport crop.
func TestRendererBufferTransform(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	// 3×2 buffer, one colour per pixel.
	colours := [2][3]color.RGBA{
		{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}},
		{{255, 255, 0, 255}, {0, 255, 255, 255}, {255, 0, 255, 255}},
	}
	var px []byte
	for y := range 2 {
		for x := range 3 {
			c := colours[y][x]
			px = append(px, c.B, c.G, c.R, 255)
		}
	}
	for _, crop := range []bool{false, true} {
		for tr := ports.BufferTransform(0); tr < 8; tr++ {
			c := shmContent(t, 3, 2, 12, px)
			// Crop: the buffer's right 2×2 pixels.
			bx0, bw, bh := 0, 3, 2
			if crop {
				c.Source = [4]float32{1, 0, 2, 2}
				bx0, bw = 1, 2
			}
			sw, sh := bw, bh
			if tr.Rotated() {
				sw, sh = bh, bw
			}
			c.Transform, c.LogicalW, c.LogicalH = tr, sw, sh
			scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: sw, H: sh}}}}
			if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: *c}); err != nil {
				t.Fatal(err)
			}
			for y := range sh {
				for x := range sw {
					bx, by := tr.ToBuffer(float64(x)+0.5, float64(y)+0.5, float64(sw), float64(sh))
					want := colours[int(by)][bx0+int(bx)]
					if got := readPixels(t, r).RGBAAt(x, y); !near(got, want, 2) {
						t.Errorf("crop=%v transform %d: surface (%d,%d) = %v, want %v", crop, tr, x, y, got, want)
					}
				}
			}
		}
	}
}
