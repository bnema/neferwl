package ports

import "math"

// HintFontPixels resolves an explicit logical size or the output's pixel density.
// Both millimetre dimensions must be plausible for the untransformed mode.
func HintFontPixels(width, height, mmWidth, mmHeight int, scale, logical float64) float64 {
	return min(maxFontPx, hintFontPixels(width, height, mmWidth, mmHeight, scale, logical))
}

// maxFontPx bounds the physical font size, whatever the configuration or the
// scale ask for.
const maxFontPx = 256

func hintFontPixels(width, height, mmWidth, mmHeight int, scale, logical float64) float64 {
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	if logical > 0 && !math.IsInf(logical, 0) {
		return logical * scale
	}
	if width > 0 && height > 0 && mmWidth > 0 && mmHeight > 0 {
		ratio := (float64(width) / float64(height)) / (float64(mmWidth) / float64(mmHeight))
		if math.Abs(ratio-1) <= .2 {
			return min(math.Round(40*scale), max(math.Round(13*scale), math.Round(3.2*float64(width)/float64(mmWidth))))
		}
	}
	return 13 * scale * math.Sqrt(max(1, float64(height)/(1080*scale)))
}
