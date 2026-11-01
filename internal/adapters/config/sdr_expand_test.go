package config

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestSDRExpansionConfig(t *testing.T) {
	c, w := parseString(t, `output.DP-1.sdr-expand = always
output.DP-1.sdr-expand-intensity = 0
output.DP-1.sdr-expand-peak = 800
output.DP-1.sdr-decode = srgb
output.DP-1 = preferred
output.DP-2 = preferred
rule.game.app-id = steam_app_\d+
rule.game.sdr-expand = off
rule.game.sdr-expand-intensity = 0.7
rule.game.sdr-expand-peak = 600
rule.game.sdr-decode = srgb
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	o := c.Outputs[0]
	if o.SDRExpand != domain.SDRExpandAlways || o.SDRExpandIntensity != 0 || o.SDRExpandPeak != 800 || o.SDRDecode != domain.SDRDecodeSRGB {
		t.Fatalf("output %+v", o)
	}
	o = c.Outputs[1]
	if o.SDRExpand != domain.SDRExpandAuto || o.SDRExpandIntensity != 0.5 || o.SDRExpandPeak != 0 || o.SDRDecode != domain.SDRDecodeGamma22 {
		t.Fatalf("defaults %+v", o)
	}
	r := c.Rules[0]
	if *r.SDRExpand != domain.SDRExpandForceOff || *r.SDRExpandIntensity != 0.7 || *r.SDRExpandPeak != 600 || *r.SDRDecode != domain.SDRDecodeSRGB {
		t.Fatalf("rule %+v", r)
	}
	for _, tc := range []struct{ field, value string }{
		{"sdr-expand", "bad"}, {"sdr-expand-intensity", "-1"}, {"sdr-expand-intensity", "1.1"}, {"sdr-expand-intensity", "NaN"}, {"sdr-expand-peak", "50"}, {"sdr-expand-peak", "20000"}, {"sdr-expand-peak", "+Inf"}, {"sdr-decode", "bad"},
	} {
		t.Run(tc.field+tc.value, func(t *testing.T) {
			_, w := parseString(t, "output.DP-1."+tc.field+" = "+tc.value+"\n")
			if len(w) != 1 {
				t.Fatalf("warnings %v", w)
			}
			_, w = parseString(t, "rule.x.app-id = x\nrule.x."+tc.field+" = "+tc.value+"\n")
			if len(w) != 1 {
				t.Fatalf("rule warnings %v", w)
			}
		})
	}
	for _, v := range []string{"game", "on", "off"} {
		if _, w := parseString(t, "rule.x.app-id = x\nrule.x.sdr-expand = "+v+"\n"); len(w) != 0 {
			t.Fatal(w)
		}
	}
	for _, v := range []string{"auto", "off", "always"} {
		if _, w := parseString(t, "output.DP-1.sdr-expand = "+v+"\n"); len(w) != 0 {
			t.Fatal(w)
		}
	}
}
