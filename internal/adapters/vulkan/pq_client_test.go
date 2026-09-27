package vulkan

import (
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A PQ 2101010 client is decoded before the SDR intermediate, which clips
// highlights above reference white. This test needs Vulkan and udmabuf.
func TestPQClientComposition(t *testing.T) {
	r, err := New(64, 4)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	format := ports.DMABufFormat{Format: fourcc('X', 'R', '3', '0'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, format) {
		t.Skip("linear XR30 unavailable")
	}
	levels := [3]float64{100, 203, 1000}
	f := udmabuf(t, 64, 4, func(x, _ int) [4]byte {
		n := levels[min(x/20, 2)]
		v := uint32(math.Round(pqEncode(n) * 1023))
		packed := v | v<<10 | v<<20
		return [4]byte{byte(packed), byte(packed >> 8), byte(packed >> 16), byte(packed >> 24)}
	})
	buf := &ports.DMABuf{ID: 51, Width: 64, Height: 4, Format: format.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	c := ports.SurfaceContent{ID: 1, Width: 64, Height: 4, Opaque: true, Color: ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}, DMABuf: buf}
	scene := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 4}}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	for i, n := range levels {
		// The test buffer contains quantized PQ codes, as real 2101010 buffers do.
		code := math.Round(pqEncode(n)*1023) / 1023
		p := math.Pow(code, 32.0/2523)
		decoded := math.Pow(math.Max(p-3424.0/4096, 0)/(2413.0/128-p*2392.0/128), 16384.0/2610) * 10000
		v := math.Min(decoded/203, 1)
		if v <= .0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - .055
		}
		want := uint8(math.Round(v * 255))
		got := r.Pixels().RGBAAt(i*20+8, 2)
		if !near(got, color.RGBA{R: want, G: want, B: want, A: 255}, 3) {
			t.Errorf("%g nits: got %v want %d", n, got, want)
		}
	}
}
