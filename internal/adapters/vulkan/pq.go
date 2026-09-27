package vulkan

import "math"

var srgbTable = func() (table [256]float64) {
	for i := range table {
		table[i] = srgbToLinear(float64(i) / 255)
	}
	return
}()

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
// The source is quantized to its 8-bit unassociated sRGB value for the LUT.
func hdrCursorPixel(p []byte, nits float64) {
	a := float64(p[3]) / 255
	if a == 0 {
		clear(p[:3])
		return
	}
	index := func(v byte) int { return min(255, int(math.Round(float64(v)/a))) }
	r, g, b := srgbTable[index(p[2])], srgbTable[index(p[1])], srgbTable[index(p[0])]
	r, g, b = pqEncode((0.627404*r+0.329283*g+0.043313*b)*nits), pqEncode((0.069097*r+0.919540*g+0.011362*b)*nits), pqEncode((0.016391*r+0.088013*g+0.895595*b)*nits)
	p[0], p[1], p[2] = byte(math.Round(b*a*255)), byte(math.Round(g*a*255)), byte(math.Round(r*a*255))
}
