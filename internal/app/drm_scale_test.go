package app

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

func TestScaleOnlyDoesNotSelectOutput(t *testing.T) {
	want := wantFromConfig([]ports.OutputConfig{{Name: "eDP-1", Scale: 2, ScaleOnly: true}, {Name: "HDMI-A-1", Mode: "1920x1080"}})
	if want.Name != "HDMI-A-1" || want.W != 1920 {
		t.Fatalf("%+v", want)
	}
}
