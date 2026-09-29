package app

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWantFromConfig(t *testing.T) {
	cfg := ports.Config{Outputs: []ports.OutputConfig{{Name: "eDP-1", Scale: 2, ScaleOnly: true, HDR: true, SDRBrightness: 350}, {Name: "HDMI-A-1", Mode: "1920x1080@60"}, {Name: "DP-3", Off: true}}}
	cfg.Render.DirectScanout, cfg.Render.VRR, cfg.Render.VRRFlipGap = true, true, 2*time.Millisecond
	want := wantFromConfig(cfg)
	if m := want.Modes["HDMI-A-1"]; m != [3]float64{1920, 1080, 60} || want.VRRFlipGap != 2*time.Millisecond || !want.Disabled["DP-3"] || len(want.Modes) != 1 || want.NoScanout || !want.NoTearing || want.NoVRR || want.HDR["eDP-1"] != (drm.HDRSettings{Enabled: true, SDRBrightness: 350}) || want.HDR["HDMI-A-1"].SDRBrightness != 203 {
		t.Fatalf("%+v", want)
	}
}
