package vulkan

import (
	"math"
	"testing"
)

func TestPQMath(t *testing.T) {
	white, green, blue := hdrPixel(1, 1, 1, 203)
	if math.Abs(white-0.5806) > 0.002 || math.Abs(green-white) > 0.0001 || math.Abs(blue-white) > 0.0001 {
		t.Fatalf("SDR white at 203 nits: %v %v %v", white, green, blue)
	}
	if pqEncode(0) != 0 || math.Abs(pqEncode(10000)-1) > 1e-9 {
		t.Fatal("PQ endpoints")
	}
	p := []byte{255, 255, 255, 255}
	hdrCursorPixel(p, 203)
	if p[3] != 255 || math.Abs(float64(p[0])-148) > 1 || p[0] != p[1] || p[1] != p[2] {
		t.Fatalf("cursor white: %v", p)
	}
}
