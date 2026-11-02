package domain

import (
	"math"
	"testing"
)

func TestSDRExpansionProperties(t *testing.T) {
	for _, nits := range [][2]float64{{604, 203}, {446, 203}, {1000, 100}, {100, 203}, {203, 203}} {
		for _, intensity := range []float64{0, 0.01, 0.25, 0.5, 1} {
			e := SDRExpansion{Intensity: intensity, PeakNits: nits[0], SDRWhiteNits: nits[1]}
			c := e.Curve()
			top := 1 + intensity*(math.Max(1, nits[0]/nits[1])-1)
			if c.Top != top || c.Apply(1) != top || c.Apply(2) != top {
				t.Fatalf("endpoint: %+v, top %g", c, top)
			}
			prev := c.Apply(0)
			for i := 0; i <= 10000; i++ {
				y := float64(i) / 10000
				got := c.Apply(y)
				if got < prev || got < y || got > top {
					t.Fatalf("nonmonotonic/unbounded: %+v y=%g got=%g prev=%g", c, y, got, prev)
				}
				if (intensity == 0 || nits[0] <= nits[1] || y <= SDRExpansionKnee) && got != y {
					t.Fatalf("not exact identity: %+v y=%g got=%g", c, y, got)
				}
				prev = got
			}
			const h = 1e-7
			left := (c.Apply(c.Knee) - c.Apply(c.Knee-h)) / h
			right := (c.Apply(c.Knee+h) - c.Apply(c.Knee)) / h
			if math.Abs(left-1) > 1e-5 || math.Abs(right-1) > 2e-5 {
				t.Fatalf("knee slope: left=%g right=%g", left, right)
			}
			if math.Abs(c.Apply(c.Knee+h)-c.Apply(c.Knee)) > 2*h {
				t.Fatal("discontinuous knee")
			}
		}
	}
}

func TestSDRExpansionRGB(t *testing.T) {
	c := (SDRExpansion{Intensity: 1, PeakNits: 604, SDRWhiteNits: 203}).Curve()
	for _, rgb := range [][3]float64{{1, 0.5, 0.25}, {0.3, 0.9, 0.6}, {0.2, 0.4, 0.8}, {0, 0, 0}, {0, 1, 0.5}, {0.1, 0.2, 0.3}} {
		r, g, b := c.ApplyRGB(rgb[0], rgb[1], rgb[2])
		out := [3]float64{r, g, b}
		m := math.Max(rgb[0], math.Max(rgb[1], rgb[2]))
		if math.Abs(math.Max(r, math.Max(g, b))-c.Apply(m)) > 1e-14 {
			t.Fatal("incorrect maximum")
		}
		for i := range 3 {
			for j := range 3 {
				if math.Abs(out[i]*rgb[j]-out[j]*rgb[i]) > 1e-14 {
					t.Fatal("channel ratio changed")
				}
			}
		}
	}
}

func TestSDRExpansionParams(t *testing.T) {
	c := (SDRExpansion{Intensity: 0.5, PeakNits: 604, SDRWhiteNits: 203, Decode: SDRDecodeSRGB}).Curve()
	want := [4]float32{float32(SDRExpansionKnee), float32(c.Top), float32(c.Shape), 1}
	if c.Params() != want {
		t.Fatalf("params %v want %v", c.Params(), want)
	}
	// Mirror the documented float32 shader arithmetic over the highlight range.
	p := c.Params()
	for i := 0; i <= 10000; i++ {
		y := float32(i) / 10000
		got := y
		if y > p[0] && p[1] != 1 {
			if y == 1 {
				got = p[1]
			} else {
				d := y - p[0]
				got = y + p[2]*d*d
			}
		}
		if math.Abs(float64(got)-c.Apply(float64(y))) > 1e-6 {
			t.Fatalf("shader mirror differs at %g", y)
		}
	}
}

func TestSDRDecode(t *testing.T) {
	for _, d := range []SDRDecode{SDRDecodeGamma22, SDRDecodeSRGB} {
		parsed, ok := ParseSDRDecode(d.String())
		if !ok || parsed != d {
			t.Fatal("decode roundtrip")
		}
		for _, v := range []float64{0, 0.01, 0.04045, 0.04046, 0.5, 1} {
			want := math.Pow(v, 2.2)
			if d == SDRDecodeSRGB {
				if v <= 0.04045 {
					want = v / 12.92
				} else {
					want = math.Pow((v+0.055)/1.055, 2.4)
				}
			}
			if DecodeSDR(v, d) != want {
				t.Fatalf("decode %v %g", d, v)
			}
		}
	}
	if _, ok := ParseSDRDecode("invalid"); ok {
		t.Fatal("accepted invalid decode")
	}
	if SDRDecode(255).String() != "unknown" {
		t.Fatal("invalid enum string")
	}
}

func TestSDRExpansionInvalidInputs(t *testing.T) {
	for _, e := range []SDRExpansion{
		{}, {Intensity: 1, PeakNits: 604}, {Intensity: 1, PeakNits: math.Inf(1), SDRWhiteNits: 203},
		{Intensity: 1, PeakNits: 604, SDRWhiteNits: math.NaN()},
		{Intensity: math.NaN(), PeakNits: 604, SDRWhiteNits: 203},
		{Intensity: -1, PeakNits: 604, SDRWhiteNits: 203},
	} {
		if c := e.Curve(); c.Apply(1) != 1 || c.Apply(0.9) != 0.9 {
			t.Fatalf("invalid input not identity: %+v", e)
		}
	}
	e := SDRExpansion{Intensity: 2, PeakNits: 604, SDRWhiteNits: 203}
	if e.Curve().Top != 604.0/203 {
		t.Fatal("intensity not clamped")
	}
}

func TestSDRExpansionCurveActive(t *testing.T) {
	if (SDRExpansionCurve{}).Active() {
		t.Fatal("zero curve active")
	}
	if (SDRExpansion{Intensity: 0, PeakNits: 604, SDRWhiteNits: 203}).Curve().Active() {
		t.Fatal("intensity 0 active")
	}
	if !(SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203}).Curve().Active() {
		t.Fatal("expansion inactive")
	}
}

func TestDecodeSDRNegativeUndershoot(t *testing.T) {
	for _, d := range []SDRDecode{SDRDecodeGamma22, SDRDecodeSRGB} {
		for _, v := range []float64{-1, -0.01, -0.000001} {
			if got := DecodeSDR(v, d); got != 0 {
				t.Errorf("DecodeSDR(%g, %s)=%g want zero", v, d, got)
			}
		}
	}
}

func TestSDRExpansionLinearSolid(t *testing.T) {
	for _, decode := range []SDRDecode{SDRDecodeGamma22, SDRDecodeSRGB} {
		active := (SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203, Decode: decode}).Curve()
		inactive := (SDRExpansion{PeakNits: 604, SDRWhiteNits: 203, Decode: decode}).Curve()
		for _, curve := range []SDRExpansionCurve{{}, inactive, active} {
			for _, plain := range []bool{false, true} {
				for _, rgb := range [][3]float64{{0, 0, 0}, {.02, .04, .01}, {.5, .25, .125}, {1, .5, .25}, {1, 1, 1}} {
					r, g, b := curve.LinearSolid(rgb[0], rgb[1], rgb[2], plain)
					got := [3]float64{r, g, b}
					want := [3]float64{}
					d := SDRDecodeSRGB
					gain := 1.0
					if plain && curve.Active() {
						d = decode
						m := DecodeSDR(rgb[0], d) // fixtures have red as their maximum
						if m > 0 {
							gain = curve.Apply(m) / m
						}
					}
					for ch := range want {
						want[ch] = DecodeSDR(rgb[ch], d) * gain
						if math.Abs(got[ch]-want[ch]) > 1e-12 {
							t.Fatalf("decode=%s active=%t plain=%t RGB=%v: got %v want %v", decode, curve.Active(), plain, rgb, got, want)
						}
					}
				}
			}
		}
	}
}
