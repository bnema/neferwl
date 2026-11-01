package domain

import (
	"math"
	"testing"
)

func TestResolveHDRCaps(t *testing.T) {
	for _, tt := range []struct {
		name   string
		in     HDRCapsInput
		tier   HDRTier
		peak   float64
		source HDRPeakSource
	}{
		{"unavailable", HDRCapsInput{Reason: "no PQ", ConfigPeakNits: 1000}, HDRUnavailable, 0, HDRPeakDefault},
		{"LG", HDRCapsInput{Capable: true, MaxLuminance: 604, MaxFrameAverage: 277, MinLuminance: 0.01}, HDRFull, 604, HDRPeakEDID},
		{"M32U", HDRCapsInput{Capable: true, MaxLuminance: 446}, HDRFull, 446, HDRPeakEDID},
		{"missing", HDRCapsInput{Capable: true}, HDRAssumed, 400, HDRPeakDefault},
		{"low", HDRCapsInput{Capable: true, MaxLuminance: 50}, HDRAssumed, 400, HDRPeakDefault},
		{"high", HDRCapsInput{Capable: true, MaxLuminance: 20000}, HDRAssumed, 400, HDRPeakDefault},
		{"nan", HDRCapsInput{Capable: true, MaxLuminance: math.NaN()}, HDRAssumed, 400, HDRPeakDefault},
		{"inf", HDRCapsInput{Capable: true, MaxLuminance: math.Inf(1)}, HDRAssumed, 400, HDRPeakDefault},
		{"override", HDRCapsInput{Capable: true, MaxLuminance: 604, ConfigPeakNits: 800}, HDRFull, 800, HDRPeakConfig},
		{"invalid override", HDRCapsInput{Capable: true, MaxLuminance: 604, ConfigPeakNits: 50}, HDRFull, 604, HDRPeakEDID},
		{"lower boundary", HDRCapsInput{Capable: true, MaxLuminance: 200}, HDRFull, 200, HDRPeakEDID},
		{"upper boundary", HDRCapsInput{Capable: true, MaxLuminance: 10000}, HDRFull, 10000, HDRPeakEDID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := ResolveHDRCaps(tt.in)
			if c.Tier != tt.tier || c.PeakNits != tt.peak || c.PeakSource != tt.source || math.Abs(c.Headroom-tt.peak/203) > 1e-9 || c.Reason == "" {
				t.Fatalf("unexpected caps: %+v", c)
			}
			if !tt.in.Capable && c.Reason != tt.in.Reason {
				t.Fatal("lost unavailable reason")
			}
		})
	}
}

func TestHDRCapsHeadroom(t *testing.T) {
	for _, white := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if c := ResolveHDRCaps(HDRCapsInput{Capable: true, MaxLuminance: 604, SDRWhiteNits: white}); math.Abs(c.Headroom-604.0/203) > 1e-9 {
			t.Fatalf("invalid white: %+v", c)
		}
	}
	for _, peak := range []float64{299, 300, 301} {
		c := ResolveHDRCaps(HDRCapsInput{Capable: true, ConfigPeakNits: peak, SDRWhiteNits: 200})
		if c.Expandable() != (peak >= 300) {
			t.Fatalf("threshold: %+v", c)
		}
	}
	if (HDRCaps{Headroom: 2}).Expandable() {
		t.Fatal("unavailable output expands")
	}
}

func TestHDRCapsStrings(t *testing.T) {
	for tier, want := range map[HDRTier]string{HDRUnavailable: "unavailable", HDRAssumed: "assumed", HDRFull: "full", HDRTier(99): "unknown"} {
		if tier.String() != want {
			t.Fatal(tier, want)
		}
	}
	for source, want := range map[HDRPeakSource]string{HDRPeakDefault: "default", HDRPeakEDID: "edid", HDRPeakConfig: "config", HDRPeakSource(99): "unknown"} {
		if source.String() != want {
			t.Fatal(source, want)
		}
	}
}
