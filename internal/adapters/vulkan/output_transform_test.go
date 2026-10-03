package vulkan

import (
	"image/color"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// The target of a rotated output holds the scene transformed by ToBuffer:
// every scene pixel of a window lands on the target pixel ToBuffer names,
// for all eight transforms, and the rest of the target stays background.
func TestRendererOutputTransform(t *testing.T) {
	const tw, th = 6, 4
	r, err := New(tw, th)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red, green := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}
	px := []byte{0, 0, 255, 255, 0, 255, 0, 255} // BGRA: red, green
	win := ports.Rect{X: 1, Y: 0, W: 2, H: 1}
	for tr := ports.BufferTransform(0); tr < 8; tr++ {
		sw, sh := r.sceneSize(tr)
		c := shmContent(t, 2, 1, 8, px)
		scene := ports.Scene{Transform: tr, Scale: 1, OutputWidth: sw, OutputHeight: sh, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: win}}}
		if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: *c}); err != nil {
			t.Fatal(err)
		}
		img := readPixels(t, r)
		want := map[[2]int]color.RGBA{}
		for i, col := range []color.RGBA{red, green} {
			bx, by := tr.ToBuffer(float64(win.X+i)+0.5, 0.5, float64(sw), float64(sh))
			want[[2]int{int(math.Floor(bx)), int(math.Floor(by))}] = col
		}
		for y := range th {
			for x := range tw {
				w, ok := want[[2]int{x, y}]
				if !ok {
					w = color.RGBA{0, 0, 0, 255}
				}
				if got := img.RGBAAt(x, y); !near(got, w, 2) {
					t.Errorf("transform %d: target (%d,%d) = %v, want %v", tr, x, y, got, w)
				}
			}
		}
	}
}

// A rotated output keeps the steady-state frame free of extra allocations:
// orient works in place.
func TestRenderSteadyStateAllocationsRotated(t *testing.T) {
	r, err := New(32, 24)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Seq: 1, Transform: 1, Scale: 1, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 24, H: 32}}}}
	content := solidContent(t, 8, 8, color.RGBA{R: 255, A: 255})
	content.ID, content.Surface, content.Seq, content.Version = 1, 1, 1, 1
	contents := map[ports.WindowID]ports.SurfaceContent{1: content}
	frame := func() {
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
	}
	frame()
	if allocs := testing.AllocsPerRun(20, frame); allocs > 13 {
		t.Errorf("rotated steady-state Render: %.1f allocs/frame, want <=13", allocs)
	} else {
		t.Logf("rotated steady-state Render: %.1f allocs/frame", allocs)
	}
}
