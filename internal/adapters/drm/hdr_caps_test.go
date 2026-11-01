package drm

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestHDRCapabilityCaps(t *testing.T) {
	c := hdrCapability{Capable: true, HDRMetadata: HDRMetadata{MaxLuminance: 604, MaxFrameAverage: 277}}
	if got := c.caps(0, 203); got.Tier != domain.HDRFull || got.PeakNits != 604 {
		t.Fatalf("caps: %+v", got)
	}
	if got := c.caps(800, 200); got.PeakSource != domain.HDRPeakConfig || got.Headroom != 4 {
		t.Fatalf("override: %+v", got)
	}
	c.Capable, c.Reason = false, "edid: no PQ"
	if got := c.caps(800, 200); got.Tier != domain.HDRUnavailable || got.Reason != c.Reason {
		t.Fatalf("unavailable: %+v", got)
	}
}
