package vulkan

import "math"

// srgbToLinear decodes a unit-range sRGB component.
func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// pqEncode encodes absolute luminance in nits using SMPTE ST 2084.
func pqEncode(nits float64) float64 {
	if nits <= 0 {
		return 0
	}
	x := math.Pow(min(nits, 10000)/10000, 2610.0/16384)
	return math.Pow((3424.0/4096+2413.0/128*x)/(1+2392.0/128*x), 2523.0/32)
}

// hdrPixel transforms an unassociated sRGB triplet into BT.2020 PQ.
func hdrPixel(red, green, blue, nits float64) (float64, float64, float64) {
	r, g, b := srgbToLinear(red), srgbToLinear(green), srgbToLinear(blue)
	return pqEncode((0.627404*r + 0.329283*g + 0.043313*b) * nits),
		pqEncode((0.069097*r + 0.919540*g + 0.011362*b) * nits),
		pqEncode((0.016391*r + 0.088013*g + 0.895595*b) * nits)
}

// hdrCursorPixel converts premultiplied BGRA bytes, preserving alpha.
func hdrCursorPixel(p []byte, nits float64) {
	a := float64(p[3]) / 255
	if a == 0 {
		clear(p[:3])
		return
	}
	r, g, b := hdrPixel(min(float64(p[2])/255/a, 1), min(float64(p[1])/255/a, 1), min(float64(p[0])/255/a, 1), nits)
	p[0], p[1], p[2] = byte(math.Round(b*a*255)), byte(math.Round(g*a*255)), byte(math.Round(r*a*255))
}
