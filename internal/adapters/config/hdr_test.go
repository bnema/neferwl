package config

import (
	"strings"
	"testing"
)

func TestHDROutputConfig(t *testing.T) {
	c, w := parseString(t, "output.DP-1.hdr = on\noutput.DP-1.sdr-brightness = 80\noutput.DP-1 = preferred\noutput.DP-2 = preferred\noutput.DP-3.hdr = off\noutput.DP-4.sdr-brightness = 1000\n")
	if len(w) != 0 || len(c.Outputs) != 4 || !c.Outputs[0].HDR || c.Outputs[0].SDRBrightness != 80 || c.Outputs[0].ScaleOnly || c.Outputs[1].HDR || c.Outputs[1].SDRBrightness != 203 || !c.Outputs[2].ScaleOnly || c.Outputs[3].SDRBrightness != 1000 {
		t.Fatalf("%+v %v", c.Outputs, w)
	}
	for _, v := range []string{"79", "1001", "-1", "abc", "1.5"} {
		c, w = parseString(t, "output.DP-1.sdr-brightness = "+v+"\n")
		if len(w) != 1 || !strings.Contains(w[0].Msg, "between 80 and 1000") || len(c.Outputs) != 0 {
			t.Fatalf("%s: %+v %v", v, c.Outputs, w)
		}
	}
	c, w = parseString(t, "output.DP-1.sdr-brightness = 300\noutput.DP-1.sdr-brightness = 79\noutput.DP-1.hdr = on\noutput.DP-1.hdr = yes\n")
	if len(w) != 2 || len(c.Outputs) != 1 || c.Outputs[0].SDRBrightness != 300 || !c.Outputs[0].HDR {
		t.Fatalf("invalid override: %+v %v", c.Outputs, w)
	}
	c, w = parseString(t, "output.DP-1.hdr = yes\n")
	if len(w) != 1 || len(c.Outputs) != 0 {
		t.Fatalf("%+v %v", c.Outputs, w)
	}
}
