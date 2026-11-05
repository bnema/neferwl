package domain

import "testing"

func TestOutputConfigHDRSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   OutputConfig
		want OutputHDRSettings
	}{
		{"zero defaults white", OutputConfig{}, OutputHDRSettings{SDRBrightness: DefaultSDRBrightness}},
		{"enabled", OutputConfig{HDR: true}, OutputHDRSettings{Enabled: true, SDRBrightness: DefaultSDRBrightness}},
		{"explicit", OutputConfig{HDR: true, SDRBrightness: 350, SDRExpandPeak: 800}, OutputHDRSettings{Enabled: true, SDRBrightness: 350, ExpansionPeakNits: 800}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.HDRSettings(); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestNewOutputHDRCaps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings OutputHDRSettings
		facts    HDRCapsInput
		active   bool
	}{
		{"inactive default white", OutputHDRSettings{}, HDRCapsInput{Reason: "off"}, false},
		{"capable config peak", OutputHDRSettings{Enabled: true, SDRBrightness: 160, ExpansionPeakNits: 800}, HDRCapsInput{Capable: true, MaxLuminance: 1000, ConfigPeakNits: 1, SDRWhiteNits: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewOutputHDRCaps("X", tc.settings, tc.facts, tc.active)
			in := tc.facts
			s := tc.settings.Normalized()
			in.ConfigPeakNits, in.SDRWhiteNits = s.ExpansionPeakNits, float64(s.SDRBrightness)
			want := OutputHDRCaps{Output: "X", Caps: ResolveHDRCaps(in), Active: tc.active}
			if got != want {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
}
