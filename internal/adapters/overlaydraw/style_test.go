package overlaydraw

import (
	"image/color"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDensitySize(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		w, h, mw, mh         int
		scale, logical, want float64
	}{
		{"ultrawide", 5120, 2160, 990, 420, 1, 0, 17},
		{"4k", 3840, 2160, 600, 340, 1.2, 0, 20},
		{"unknown", 1920, 1080, 0, 0, 1, 0, 13},
		{"bogus aspect", 1920, 1080, 100, 100, 1, 0, 13},
		{"fallback high", 5120, 2160, 0, 0, 1, 0, 18.38477631085024},
		{"minimum", 1920, 1080, 1000, 563, 1.2, 0, 16},
		{"maximum", 5120, 2160, 100, 42, 1, 0, 40},
		{"override", 5120, 2160, 990, 420, 1.2, 24, 28.8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.InDelta(t, tt.want, FontPixels(tt.w, tt.h, tt.mw, tt.mh, tt.scale, tt.logical), .00001)
		})
	}
}

func TestDerivedContrast(t *testing.T) {
	for _, s := range []string{"#f9e2af", "#89b4fa", "#808080", "#000000", "#ffffff", "#1e1e2e", "#747474", "#757575", "#ff0000", "#00ff00", "#0000ff"} {
		t.Run(s, func(t *testing.T) {
			bg, fg := Colors(s, "", "")
			require.GreaterOrEqual(t, contrast(bg, fg), 4.5)
			require.Contains(t, []color.RGBA{{17, 17, 17, 255}, {245, 245, 245, 255}}, fg)
		})
	}
	// Sweep the gap between near-black and near-white contrast thresholds.
	for i := 0; i < 256; i++ {
		c := color.RGBA{uint8(i), uint8(i), uint8(i), 255}
		s := "#" + hexByte(c.R) + hexByte(c.G) + hexByte(c.B)
		bg, fg := Colors(s, "", "")
		require.GreaterOrEqual(t, contrast(bg, fg), 4.5, s)
	}
}

func hexByte(v uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&15]})
}

func TestOverridesAndTyped(t *testing.T) {
	bg, fg := Colors("#ffffff", "#000000", "#89b4fa")
	require.Equal(t, color.RGBA{0, 0, 0, 255}, bg)
	require.Equal(t, color.RGBA{137, 180, 250, 255}, fg)
	require.Equal(t, color.RGBA{62, 81, 113, 255}, mix(fg, bg))
	fallback, _ := Colors("", "", "")
	require.Equal(t, color.RGBA{128, 128, 128, 255}, fallback)
}

func TestStyledRasterSizeAndCache(t *testing.T) {
	var r Raster
	bg, fg := Colors("#f9e2af", "", "")
	style := Style{13, bg, fg}
	a, _, small := r.Label("fj", 0, 1, style)
	style.FontPx = 26
	b, _, large := r.Label("fj", 0, 1, style)
	require.Greater(t, large, small)
	require.NotEqual(t, a, b)
	style.Background = color.RGBA{0, 0, 0, 255}
	c, _, _ := r.Label("fj", 0, 1, style)
	require.NotEqual(t, b, c)
	require.Equal(t, byte(89), c[3], "outline has 35 percent physical-pixel alpha")
}

func TestFontPixelsClamped(t *testing.T) {
	require.InDelta(t, 256, FontPixels(1920, 1080, 0, 0, 3, 1e9), .00001)
	require.InDelta(t, 256, FontPixels(1920, 1080, 0, 0, 4, 72), .00001)
}
