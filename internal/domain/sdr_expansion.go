package domain

import "math"

// SDRDecode selects the transfer function for expanded SDR surfaces only.
type SDRDecode uint8

const (
	SDRDecodeGamma22 SDRDecode = iota
	SDRDecodeSRGB
)

func (d SDRDecode) String() string {
	switch d {
	case SDRDecodeGamma22:
		return "gamma22"
	case SDRDecodeSRGB:
		return "srgb"
	default:
		return "unknown"
	}
}

// ParseSDRDecode accepts the canonical configuration names. The boolean is
// false for an unknown name; the returned fallback is SDRDecodeGamma22.
func ParseSDRDecode(s string) (SDRDecode, bool) {
	switch s {
	case "gamma22":
		return SDRDecodeGamma22, true
	case "srgb":
		return SDRDecodeSRGB, true
	default:
		return SDRDecodeGamma22, false
	}
}

// SDRExpansionKnee is the highest input that remains unchanged.
const SDRExpansionKnee = 0.7

// SDRExpansion describes expansion in linear SDR-white units. Nit values must
// be finite and SDRWhiteNits positive. Intensity is clamped to [0, 1].
type SDRExpansion struct {
	Intensity    float64
	PeakNits     float64
	SDRWhiteNits float64
	Decode       SDRDecode
}

// SDRExpansionCurve holds per-draw constants derived by SDRExpansion.Curve.
// Shape is the nonnegative quadratic coefficient, not an extra gain.
type SDRExpansionCurve struct {
	Knee, MaxOut, Top, Shape float64
	Decode                   SDRDecode
}

// Curve derives a quadratic highlight segment with slope 1 at the knee.
// Invalid luminance values or NaN intensity fail closed to identity.
func (e SDRExpansion) Curve() SDRExpansionCurve {
	maxOut := 1.0
	if e.SDRWhiteNits > 0 && !math.IsInf(e.SDRWhiteNits, 0) && !math.IsNaN(e.PeakNits) && !math.IsInf(e.PeakNits, 0) {
		ratio := e.PeakNits / e.SDRWhiteNits
		if !math.IsInf(ratio, 0) {
			maxOut = math.Max(1, ratio)
		}
	}
	intensity := e.Intensity
	if math.IsNaN(intensity) {
		intensity = 0
	}
	intensity = math.Max(0, math.Min(1, intensity))
	top := 1 + intensity*(maxOut-1)
	return SDRExpansionCurve{
		Knee: SDRExpansionKnee, MaxOut: maxOut, Top: top,
		Shape:  (top - 1) / ((1 - SDRExpansionKnee) * (1 - SDRExpansionKnee)),
		Decode: e.Decode,
	}
}

// Active reports whether the curve changes any value: the zero curve (no
// expansion) and intensity zero (Top 1) are both inactive.
func (c SDRExpansionCurve) Active() bool { return c.Top > 1 }

// Params returns the shader vec4: (knee, top, quadratic coefficient, decode).
// Decode is 0 for gamma 2.2, 1 for piecewise sRGB.
func (c SDRExpansionCurve) Params() [4]float32 {
	return [4]float32{float32(c.Knee), float32(c.Top), float32(c.Shape), float32(c.Decode)}
}

// Apply maps linear luminance in SDR-white units. Inputs above 1 are clamped;
// values at or below the knee are exact, including at intensity zero.
// The exact GLSL mirror, with p = Params(), is:
//
// y = min(y, 1.0);
// if (y <= p.x || p.y == 1.0) return y;
// if (y == 1.0) return p.y;
// float t = y - p.x;
// return y + p.z * t * t;
func (c SDRExpansionCurve) Apply(y float64) float64 {
	y = math.Min(y, 1)
	if y <= c.Knee || c.Top == 1 {
		return y
	}
	if y == 1 {
		return c.Top
	}
	t := y - c.Knee
	return y + c.Shape*t*t
}

// ApplyRGB applies a common gain Apply(m)/m on the maximum component m.
// Linear RGB channel ratios (and thus hue) are preserved, unlike independent
// channel expansion. Black is unchanged. SDR inputs are expected in [0, 1].
func (c SDRExpansionCurve) ApplyRGB(r, g, b float64) (float64, float64, float64) {
	m := math.Max(r, math.Max(g, b))
	if m <= 0 {
		return r, g, b
	}
	gain := c.Apply(m) / m
	return r * gain, g * gain, b * gain
}

// DecodeSDR converts an encoded SDR component in [0, 1] to linear light.
// Negative reconstruction undershoot is clamped to zero before either decode.
// SDRDecodeGamma22 is a pure power; SDRDecodeSRGB uses the standard piecewise transfer function.
func DecodeSDR(v float64, d SDRDecode) float64 {
	v = math.Max(v, 0)
	if d == SDRDecodeSRGB {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return math.Pow(v, 2.2)
}

// LinearSolid decodes straight, encoded single-pixel RGB in [0, 1] to linear
// light. Only plain SDR with an active curve uses the selected expansion
// decode and highlight gain; other solids retain ordinary sRGB decoding.
// Premultiplication belongs after this conversion.
func (c SDRExpansionCurve) LinearSolid(r, g, b float64, plainSDR bool) (lr, lg, lb float64) {
	if plainSDR && c.Active() {
		return c.ApplyRGB(DecodeSDR(r, c.Decode), DecodeSDR(g, c.Decode), DecodeSDR(b, c.Decode))
	}
	return DecodeSDR(r, SDRDecodeSRGB), DecodeSDR(g, SDRDecodeSRGB), DecodeSDR(b, SDRDecodeSRGB)
}
