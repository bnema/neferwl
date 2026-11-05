package domain

// OutputHDRSettings is the requested HDR state of an output, shared by every
// backend. SDRBrightness is the SDR reference white in nits (zero: default);
// ExpansionPeakNits overrides the detected peak (zero: resolve from facts).
type OutputHDRSettings struct {
	ExpansionPeakNits float64
	Enabled           bool
	SDRBrightness     int
}

// Normalized fills the default SDR reference white.
func (s OutputHDRSettings) Normalized() OutputHDRSettings {
	if s.SDRBrightness == 0 {
		s.SDRBrightness = DefaultSDRBrightness
	}
	return s
}

// HDRSettings returns the output's normalized HDR settings.
func (o OutputConfig) HDRSettings() OutputHDRSettings {
	return OutputHDRSettings{Enabled: o.HDR, SDRBrightness: o.SDRBrightness, ExpansionPeakNits: o.SDRExpandPeak}.Normalized()
}

// NewOutputHDRCaps resolves an output's reported caps from its capability
// facts (Capable, Reason, luminances) and settings; settings supply the
// configured peak and SDR white, overriding those fields of facts.
func NewOutputHDRCaps(output string, settings OutputHDRSettings, facts HDRCapsInput, active bool) OutputHDRCaps {
	settings = settings.Normalized()
	facts.ConfigPeakNits = settings.ExpansionPeakNits
	facts.SDRWhiteNits = float64(settings.SDRBrightness)
	return OutputHDRCaps{Output: output, Caps: ResolveHDRCaps(facts), Active: active}
}
