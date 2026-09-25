package app

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

func TestWantFromConfig(t *testing.T) {
	cfg := ports.Config{Outputs: []ports.OutputConfig{{Name: "eDP-1", Scale: 2, ScaleOnly: true}, {Name: "HDMI-A-1", Mode: "1920x1080@60"}, {Name: "DP-3", Off: true}}}
	cfg.Render.DirectScanout, cfg.Render.VRR = true, true
	want := wantFromConfig(cfg)
	if m := want.Modes["HDMI-A-1"]; m != [3]float64{1920, 1080, 60} || !want.Disabled["DP-3"] || len(want.Modes) != 1 || want.NoScanout || !want.NoTearing || want.NoVRR {
		t.Fatalf("%+v", want)
	}
}
