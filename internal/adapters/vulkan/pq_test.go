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
}
