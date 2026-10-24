package hintlabel

import (
	"github.com/bnema/neferwl/internal/ports"
	"image/color"
	"math"
	"strconv"
)

// Style is the resolved, physical-pixel label style and is also a cache key.
type Style struct {
	FontPx                 float64
	Background, Foreground color.RGBA
}

// FontPixels resolves the shared physical font size.
func FontPixels(width, height, mmWidth, mmHeight int, scale, logical float64) float64 {
	return ports.HintFontPixels(width, height, mmWidth, mmHeight, scale, logical)
}

func parseColor(s string, fallback color.RGBA) color.RGBA {
	if len(s) != 7 || s[0] != '#' {
		return fallback
	}
	n, err := strconv.ParseUint(s[1:], 16, 24)
	if err != nil {
		return fallback
	}
	return color.RGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), 255}
}

func luminance(c color.RGBA) float64 {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= .04045 {
			return x / 12.92
		}
		return math.Pow((x+.055)/1.055, 2.4)
	}
	return .2126*linear(c.R) + .7152*linear(c.G) + .0722*linear(c.B)
}

func contrast(a, b color.RGBA) float64 {
	x, y := luminance(a), luminance(b)
	return (max(x, y) + .05) / (min(x, y) + .05)
}

// Colors derives ink from the background. Near-black and near-white have a
// narrow luminance gap where neither meets AA; in that gap move HSL
// lightness away from .5, preserving hue and saturation.
// Explicit foreground overrides are honoured as supplied.
func Colors(active, background, foreground string) (color.RGBA, color.RGBA) {
	bg := parseColor(background, parseColor(active, color.RGBA{128, 128, 128, 255}))
	dark, light := color.RGBA{17, 17, 17, 255}, color.RGBA{245, 245, 245, 255}
	ink := dark
	if contrast(bg, light) > contrast(bg, dark) {
		ink = light
	}
	for contrast(bg, ink) < 4.5 {
		// Move HSL lightness toward zero while preserving hue and saturation.
		hi, lo := max(bg.R, bg.G, bg.B), min(bg.R, bg.G, bg.B)
		l := (float64(hi) + float64(lo)) / 510
		if l > .5 {
			// Above .5, moving toward white increases contrast with dark ink.
			bg.R += uint8(math.Ceil(float64(255-bg.R) * .01))
			bg.G += uint8(math.Ceil(float64(255-bg.G) * .01))
			bg.B += uint8(math.Ceil(float64(255-bg.B) * .01))
			ink = dark
		} else {
			bg.R = uint8(float64(bg.R) * .99)
			bg.G = uint8(float64(bg.G) * .99)
			bg.B = uint8(float64(bg.B) * .99)
			ink = light
		}
	}
	return bg, parseColor(foreground, ink)
}

func mix(ink, bg color.RGBA) color.RGBA {
	channel := func(a, b uint8) uint8 { return uint8(math.Round(.45*float64(a) + .55*float64(b))) }
	return color.RGBA{channel(ink.R, bg.R), channel(ink.G, bg.G), channel(ink.B, bg.B), 255}
}
