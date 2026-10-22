package vulkan

import (
	"math"

	"github.com/bnema/neferwl/internal/adapters/hdrcursor"
)

// srgbToLinear decodes a unit-range sRGB component.
func srgbToLinear(v float64) float64 { return hdrcursor.SRGBToLinear(v) }

// linearToSRGB encodes a unit-range linear component; the inverse of
// srgbToLinear.
func linearToSRGB(v float64) float64 {
	if v <= 0.04045/12.92 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// pqEncode encodes absolute luminance in nits using SMPTE ST 2084.
func pqEncode(nits float64) float64 { return hdrcursor.PQEncode(nits) }

// hdrPixel transforms an unassociated sRGB triplet into BT.2020 PQ.
func hdrPixel(red, green, blue, nits float64) (float64, float64, float64) {
	r, g, b := srgbToLinear(red), srgbToLinear(green), srgbToLinear(blue)
	return pqEncode((0.627404*r + 0.329283*g + 0.043313*b) * nits),
		pqEncode((0.069097*r + 0.919540*g + 0.011362*b) * nits),
		pqEncode((0.016391*r + 0.088013*g + 0.895595*b) * nits)
}
