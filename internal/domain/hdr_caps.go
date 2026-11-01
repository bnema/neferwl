package domain

import "math"

// OutputHDRCaps reports output capability facts and confirmed HDR10 signaling.
type OutputHDRCaps struct {
	Output string
	Caps   HDRCaps
	Active bool
}

// HDRTier describes confidence in an output's HDR peak, not its active mode.
type HDRTier uint8

const (
	HDRUnavailable HDRTier = iota
	HDRAssumed
	HDRFull
)

func (t HDRTier) String() string {
	switch t {
	case HDRUnavailable:
		return "unavailable"
	case HDRAssumed:
		return "assumed"
	case HDRFull:
		return "full"
	default:
		return "unknown"
	}
}

// HDRPeakSource identifies where the resolved peak came from.
type HDRPeakSource uint8

const (
	HDRPeakDefault HDRPeakSource = iota
	HDRPeakEDID
	HDRPeakConfig
)

func (s HDRPeakSource) String() string {
	switch s {
	case HDRPeakDefault:
		return "default"
	case HDRPeakEDID:
		return "edid"
	case HDRPeakConfig:
		return "config"
	default:
		return "unknown"
	}
}

const (
	DefaultAssumedPeakNits = 400
	MinExpansionHeadroom   = 1.5
)

// HDRCapsInput contains output facts; all luminances are in nits, with zero
// meaning unspecified. Frame-average and minimum luminance do not set the peak.
type HDRCapsInput struct {
	Capable                                     bool
	Reason                                      string
	MaxLuminance, MaxFrameAverage, MinLuminance float64
	ConfigPeakNits, SDRWhiteNits                float64
}

// HDRCaps is independent of whether HDR signaling is currently enabled.
// Peak and headroom are zero for unavailable outputs.
type HDRCaps struct {
	Tier       HDRTier
	PeakNits   float64
	PeakSource HDRPeakSource
	Headroom   float64
	Reason     string
}

func (c HDRCaps) Expandable() bool {
	return c.Tier != HDRUnavailable && c.Headroom >= MinExpansionHeadroom
}

// ResolveHDRCaps resolves config, EDID, then an assumed peak. Invalid peaks
// (including non-finite values) are ignored. Invalid reference white uses 203.
func ResolveHDRCaps(in HDRCapsInput) HDRCaps {
	if !in.Capable {
		return HDRCaps{Reason: in.Reason}
	}
	c := HDRCaps{Tier: HDRFull}
	sane := func(n float64) bool { return n >= 200 && n <= 10000 }
	switch {
	case sane(in.ConfigPeakNits):
		c.PeakNits, c.PeakSource, c.Reason = in.ConfigPeakNits, HDRPeakConfig, "config peak"
	case sane(in.MaxLuminance):
		c.PeakNits, c.PeakSource, c.Reason = in.MaxLuminance, HDRPeakEDID, "EDID peak"
	default:
		c.Tier = HDRAssumed
		c.PeakNits, c.PeakSource, c.Reason = DefaultAssumedPeakNits, HDRPeakDefault, "missing or invalid peak: assumed 400 nits"
	}
	white := in.SDRWhiteNits
	if white <= 0 || math.IsNaN(white) || math.IsInf(white, 0) {
		white = 203
	}
	c.Headroom = c.PeakNits / white
	return c
}
