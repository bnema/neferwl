package pattern

import (
	"image"
	"math"
	"testing"
)

func TestPatchesTileTheSurface(t *testing.T) {
	for _, mode := range []string{"sdr", "hdr"} {
		for _, size := range [][2]int{{640, 360}, {641, 361}} {
			w, h := size[0], size[1]
			patches := Patches(mode, w, h)
			if len(patches) != Columns*Rows {
				t.Fatalf("%s %dx%d: %d patches", mode, w, h, len(patches))
			}
			covered := make([]int, w*h)
			for _, p := range patches {
				if !p.Rect.In(image.Rect(0, 0, w, h)) || p.Rect.Empty() {
					t.Fatalf("%s %dx%d: bad rect %v", mode, w, h, p.Rect)
				}
				for y := p.Rect.Min.Y; y < p.Rect.Max.Y; y++ {
					for x := p.Rect.Min.X; x < p.Rect.Max.X; x++ {
						covered[y*w+x]++
					}
				}
			}
			for i, n := range covered {
				if n != 1 {
					t.Fatalf("%s %dx%d: pixel (%d,%d) covered %d times", mode, w, h, i%w, i/w, n)
				}
			}
		}
	}
}

func TestPatchesModes(t *testing.T) {
	if Patches("bogus", 640, 360) != nil {
		t.Fatal("unknown mode must yield nil")
	}
	sdr := Patches("sdr", 600, 200)
	if sdr[5].SRGB != [3]uint8{255, 128, 0} || sdr[11].SRGB != [3]uint8{255, 255, 0} {
		t.Fatalf("sdr patches: %v", sdr)
	}
	hdr := Patches("hdr", 600, 200)
	if hdr[3].Nits != [3]float64{203, 203, 203} || hdr[5].Nits != [3]float64{1000, 1000, 1000} {
		t.Fatalf("hdr neutrals: %v", hdr)
	}
	// BT.709 red at 203 nits in BT.2020.
	want := [3]float64{0.627404 * 203, 0.069097 * 203, 0.016391 * 203}
	for i, v := range hdr[9].Nits {
		if math.Abs(v-want[i]) > 1e-9 {
			t.Fatalf("709 red = %v, want %v", hdr[9].Nits, want)
		}
	}
	if c := sdr[0].Centre(); c != image.Pt(50, 50) {
		t.Fatalf("centre = %v", c)
	}
}

func TestPQEncode(t *testing.T) {
	if v := PQEncode(203); math.Abs(v-0.5807) > 1e-4 {
		t.Fatalf("PQEncode(203) = %v", v)
	}
	if v := PQEncode(10000); math.Abs(v-1) > 1e-4 {
		t.Fatalf("PQEncode(10000) = %v", v)
	}
	if PQEncode(0) != 0 || PQEncode(-1) != 0 {
		t.Fatal("PQEncode must clamp to zero")
	}
}

func TestSRGBToLinear(t *testing.T) {
	if SRGBToLinear(0) != 0 || math.Abs(SRGBToLinear(1)-1) > 1e-12 {
		t.Fatal("endpoints")
	}
	if v := SRGBToLinear(128.0 / 255); math.Abs(v-0.2158605) > 1e-6 {
		t.Fatalf("SRGBToLinear(128/255) = %v", v)
	}
}
