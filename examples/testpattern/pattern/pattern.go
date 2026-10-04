// Package pattern defines the colour patches of the testpattern client and
// the transfer functions that say what each patch must look like. It has no
// Wayland imports so tests can reuse it as an oracle; it is deliberately
// independent of the renderer's own colour code.
package pattern

import (
	"image"
	"math"
)

const (
	// Columns and Rows give the patch grid.
	Columns = 6
	Rows    = 2
)

// Patch is one solid rectangle of the test pattern. SRGB is set in "sdr"
// mode, Nits (linear BT.2020, absolute luminance) in "hdr" mode.
type Patch struct {
	Rect image.Rectangle
	SRGB [3]uint8
	Nits [3]float64
}

// Centre returns the middle of the patch.
func (p Patch) Centre() image.Point {
	return image.Pt((p.Rect.Min.X+p.Rect.Max.X)/2, (p.Rect.Min.Y+p.Rect.Max.Y)/2)
}

// sdrColours are the sRGB patches in row-major order.
var sdrColours = [Columns * Rows][3]uint8{
	{0, 0, 0}, {64, 64, 64}, {128, 128, 128}, {192, 192, 192}, {255, 255, 255}, {255, 128, 0},
	{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {0, 255, 255}, {255, 0, 255}, {255, 255, 0},
}

// BT709ToBT2020 converts linear BT.709 RGB to linear BT.2020 RGB.
var BT709ToBT2020 = [3][3]float64{
	{0.627404, 0.329283, 0.043313},
	{0.069097, 0.919540, 0.011362},
	{0.016391, 0.088013, 0.895595},
}

// ReferenceWhite is the luminance of SDR white in an HDR signal, in nits.
const ReferenceWhite = 203

// hdrNits are the BT.2020 linear patches in row-major order.
var hdrNits = func() (n [Columns * Rows][3]float64) {
	for i, v := range []float64{0, 1, 100, ReferenceWhite, 400, 1000} {
		n[i] = [3]float64{v, v, v}
	}
	n[6] = [3]float64{ReferenceWhite, 0, 0}
	n[7] = [3]float64{0, ReferenceWhite, 0}
	n[8] = [3]float64{0, 0, ReferenceWhite}
	for c := range 3 {
		var v [3]float64
		for r := range 3 {
			v[r] = BT709ToBT2020[r][c] * ReferenceWhite
		}
		n[9+c] = v
	}
	return
}()

// Patches lays the mode's patches out as a 6×2 grid filling w×h. Cell sizes
// use integer division; the last column and row take the remainder. Mode is
// "sdr" or "hdr"; any other value yields nil.
func Patches(mode string, w, h int) []Patch {
	if mode != "sdr" && mode != "hdr" {
		return nil
	}
	patches := make([]Patch, 0, Columns*Rows)
	for i := range Columns * Rows {
		col, row := i%Columns, i/Columns
		x0, x1 := col*(w/Columns), (col+1)*(w/Columns)
		y0, y1 := row*(h/Rows), (row+1)*(h/Rows)
		if col == Columns-1 {
			x1 = w
		}
		if row == Rows-1 {
			y1 = h
		}
		p := Patch{Rect: image.Rect(x0, y0, x1, y1)}
		if mode == "sdr" {
			p.SRGB = sdrColours[i]
		} else {
			p.Nits = hdrNits[i]
		}
		patches = append(patches, p)
	}
	return patches
}

// PQEncode encodes absolute luminance in nits with SMPTE ST 2084, clamped to
// 0–10000 nits, and returns a unit-range signal.
func PQEncode(nits float64) float64 {
	if nits <= 0 {
		return 0
	}
	x := math.Pow(min(nits, 10000)/10000, 2610.0/16384)
	return math.Pow((3424.0/4096+2413.0/128*x)/(1+2392.0/128*x), 2523.0/32)
}

// SRGBToLinear decodes a unit-range sRGB component.
func SRGBToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}
