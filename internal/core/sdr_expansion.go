package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"math"
)

type sdrExpansionInput struct {
	Output                  domain.OutputHDRCaps
	Config                  domain.OutputConfig
	Rule                    ruleEffect
	Fullscreen, Game, Popup bool
	ColorDescription        bool
}

// decideSDRExpansion is pure. Overrides never bypass output HDR capability;
// forced eligibility still needs a peak above reference white.
func decideSDRExpansion(in sdrExpansionInput) (domain.SDRExpansionCurve, string) {
	off := domain.SDRExpansionCurve{}
	if in.Popup {
		return off, "popup"
	}
	if in.ColorDescription {
		return off, "colour-managed surface"
	}
	if !in.Output.Active {
		return off, "output not HDR"
	}
	if in.Output.Caps.Tier == domain.HDRUnavailable {
		return off, "unavailable"
	}
	if in.Config.SDRExpand == domain.SDRExpandOff {
		return off, "mode off"
	}
	r := in.Rule
	if r.sdrExpand != nil && *r.sdrExpand == domain.SDRExpandForceOff {
		return off, "rule off"
	}
	peak := in.Output.Caps.PeakNits
	if in.Config.SDRExpandPeak > 0 {
		peak = in.Config.SDRExpandPeak
	}
	if r.sdrExpandPeak != nil && *r.sdrExpandPeak > 0 {
		peak = *r.sdrExpandPeak
	}
	white := float64(in.Config.SDRBrightness)
	if white <= 0 {
		white = domain.DefaultSDRBrightness
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak < 200 || peak > 10000 {
		return off, "invalid peak"
	}
	forced := in.Config.SDRExpand == domain.SDRExpandAlways || r.sdrExpand != nil && *r.sdrExpand == domain.SDRExpandOn
	headroom := peak / white
	if headroom <= 1 || !forced && headroom < domain.MinExpansionHeadroom {
		return off, "insufficient headroom"
	}
	if !forced {
		if !in.Fullscreen {
			return off, "not fullscreen"
		}
		if !in.Game && !(r.sdrExpand != nil && *r.sdrExpand == domain.SDRExpandGame) {
			return off, "not game"
		}
	}
	intensity, decode := in.Config.SDRExpandIntensity, in.Config.SDRDecode
	if r.sdrExpandIntensity != nil {
		intensity = *r.sdrExpandIntensity
	}
	if r.sdrDecode != nil {
		decode = *r.sdrDecode
	}
	curve := (domain.SDRExpansion{Intensity: intensity, PeakNits: peak, SDRWhiteNits: white, Decode: decode}).Curve()
	if !curve.Active() {
		return off, "zero intensity"
	}
	return curve, "eligible"
}

func (c *Core) matchWindowExpansion(id WindowID) {
	if rec := c.windows.records[id]; rec != nil && rec.client.ID != 0 {
		rec.expansionRule, _ = matchRules(c.rules, rec.client.AppID)
	}
}
