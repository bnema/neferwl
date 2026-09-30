package vulkan

import (
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A PQ 2101010 client is decoded before the SDR intermediate, which clips
// highlights above reference white. The GPU and udmabuf are optional.
func TestPQClientComposition(t *testing.T) {
	r, err := New(64, 4)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	type sample struct {
		rgb   [3]float64
		alpha float64
	}
	// Neutral values test absolute luminance; saturated colors exercise the
	// BT.2020 -> BT.709 matrix (and the clipping of out-of-gamut channels).
	samples := []sample{
		{[3]float64{100, 100, 100}, 1}, {[3]float64{203, 203, 203}, 1}, {[3]float64{1000, 1000, 1000}, 1},
		{[3]float64{100, 0, 0}, 1}, {[3]float64{0, 0, 100}, 1}, {[3]float64{0, 100, 0}, .6666667},
	}
	for _, name := range [][4]byte{{'X', 'R', '3', '0'}, {'X', 'B', '3', '0'}, {'A', 'B', '3', '0'}} {
		t.Run(string(name[:]), func(t *testing.T) {
			format := ports.DMABufFormat{Format: fourcc(name[0], name[1], name[2], name[3]), Modifier: 0}
			if !slices.Contains(r.DMABuf().Formats, format) {
				t.Skipf("linear %s unavailable", name)
			}
			// Store premultiplied PQ codes for alpha buffers. Alpha uses two bits.
			f := udmabuf(t, 64, 4, func(x, _ int) [4]byte {
				s := samples[min(x/10, len(samples)-1)]
				a := uint32(math.Round(s.alpha * 3))
				if name[0] == 'X' {
					a = 0 // X bits are undefined, and must be ignored.
					s.alpha = 1
				}
				var ch [3]uint32
				for i, n := range s.rgb {
					ch[i] = uint32(math.Round(pqEncode(n) * s.alpha * 1023))
				}
				var packed uint32
				if name[1] == 'R' {
					packed = ch[2] | ch[1]<<10 | ch[0]<<20
				} else {
					packed = ch[0] | ch[1]<<10 | ch[2]<<20
				}
				packed |= a << 30
				return [4]byte{byte(packed), byte(packed >> 8), byte(packed >> 16), byte(packed >> 24)}
			})
			buf := &ports.DMABuf{ID: uint64(format.Format), Width: 64, Height: 4, Format: format.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
			c := ports.SurfaceContent{ID: 1, Width: 64, Height: 4, Opaque: name[0] == 'X', Color: ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}, DMABuf: buf}
			scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 4}}}}
			if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
				t.Fatal(err)
			}
			for i, s := range samples {
				alpha := s.alpha
				if name[0] == 'X' {
					alpha = 1
				}
				var nits [3]float64
				for j, n := range s.rgb {
					sourceAlpha := s.alpha
					if name[0] == 'X' {
						sourceAlpha = 1
					}
					code := math.Round(pqEncode(n)*sourceAlpha*1023) / 1023
					p := math.Pow(code/sourceAlpha, 32.0/2523)
					nits[j] = math.Pow(math.Max(p-3424.0/4096, 0)/(2413.0/128-p*2392.0/128), 16384.0/2610) * 10000
				}
				linear := [3]float64{
					1.660491*nits[0] - 0.587641*nits[1] - 0.072850*nits[2],
					-0.124550*nits[0] + 1.132900*nits[1] - 0.008349*nits[2],
					-0.018151*nits[0] - 0.100579*nits[1] + 1.118730*nits[2],
				}
				want := color.RGBA{A: 255}
				channels := []*uint8{&want.R, &want.G, &want.B}
				for j, n := range linear {
					v := math.Max(0, math.Min(n/203, 1))
					if v <= .0031308 {
						v *= 12.92
					} else {
						v = 1.055*math.Pow(v, 1/2.4) - .055
					}
					// Composition blends premultiplied source onto a black opaque fill.
					*channels[j] = uint8(math.Round(v * alpha * 255))
				}
				got := readPixels(t, r).RGBAAt(i*10+5, 2)
				if !near(got, want, 4) {
					t.Errorf("sample %d: got %v want %v", i, got, want)
				}
			}
		})
	}
}
