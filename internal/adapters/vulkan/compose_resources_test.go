package vulkan

import (
	"image/color"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A mixed resource table must select SHM pixels, RGB images, and both YUV
// planes independently within the same draw call.
func TestComposeIndexedMixedResources(t *testing.T) {
	r := newTestRenderer(t, 64, 16)
	defer r.Close()
	if !r.compose.indexing || r.compose.resources < 3 {
		t.Skip("nonuniform indexing of three resources unavailable")
	}
	yuv := yuvTestBuffer(t, false, 63, 102, 240)
	if !slices.Contains(r.DMABuf().Formats, domain.DMABufFormat{Format: yuv.Format}) {
		t.Skip("linear NV12 not importable")
	}
	rgbFormat := fourcc('A', 'R', '2', '4')
	if !slices.Contains(r.DMABuf().Formats, domain.DMABufFormat{Format: rgbFormat}) {
		t.Skip("linear ARGB not importable")
	}
	rgb := &domain.DMABuf{ID: 991, Width: 64, Height: 16, Format: rgbFormat, Planes: []domain.DMABufPlane{{File: udmabuf(t, 64, 16, func(int, int) [4]byte { return [4]byte{255, 0, 0, 255} }), Stride: 256}}}
	shm := solidContent(t, 64, 16, color.RGBA{G: 255, A: 255})
	shm.ID, shm.Seq, shm.Version = 2, 1, 1
	scene := ports.Scene{Layers: []ports.SceneLayer{
		{ID: 1, Layer: domain.LayerOverlay, Rect: domain.Rect{W: 64, H: 16}},
		{ID: 2, Layer: domain.LayerOverlay, Rect: domain.Rect{X: 16, W: 32, H: 16}},
		{ID: 3, Layer: domain.LayerOverlay, Rect: domain.Rect{X: 32, W: 16, H: 16}},
	}}
	contents := map[domain.WindowID]domain.SurfaceContent{
		1: {ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: yuv, Color: domain.SurfaceColor{Coefficients: 2, Range: 2, Chroma: 1}},
		2: shm,
		3: {ID: 3, Width: 64, Height: 16, Opaque: true, DMABuf: rgb},
	}
	for range 4 {
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
		pixels := readPixels(t, r)
		for _, tc := range []struct {
			x    int
			want color.RGBA
		}{{8, color.RGBA{R: 255, A: 255}}, {24, color.RGBA{G: 255, A: 255}}, {40, color.RGBA{B: 255, A: 255}}} {
			if got := pixels.RGBAAt(tc.x, 8); !near(got, tc.want, 8) {
				t.Fatalf("resource at x=%d: %v, want %v", tc.x, got, tc.want)
			}
		}
	}
	for i := range r.slots {
		if len(r.slots[i].batches) != 1 || r.slots[i].batches[0].count != 3 {
			t.Fatalf("slot %d did not batch the three resource modes", i)
		}
	}
}
