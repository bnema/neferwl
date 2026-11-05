package config

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestVirtualOutputModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		valid      bool
	}{
		{"tv", "1920x1080", true},
		{"TV-1_test.2", "1920x1080@59.94", true},
		{"min", "1x1@1", true},
		{"max", "16384x16384@120", true},
		{"tv", "preferred", false},
		{"tv", "off", false},
		{"tv", "", false},
		{"tv", "foo", false},
		{"tv", "0x10", false},
		{"tv", "10x0", false},
		{"tv", "-1x10", false},
		{"tv", "16385x10", false},
		{"tv", "10x16385", false},
		{"tv", "999999999999999999999x10", false},
		{"tv", "10x10@0", false},
		{"tv", "10x10@0.0001", false},
		{"tv", "10x10@1e100", false},
		{"tv", "10x10@2147483.648", false},
		{"tv", "10x10@2147483.647", true},
		{"tv", "10x10@0.0005", true},
		{"tv", "10x10@-60", false},
		{"tv", "10x10@NaN", false},
		{"tv", "10x10@Inf", false},
		{"tv", "10x10@", false},
		{"tv", "10x10 extra", false},
		{"bad/name", "10x10", false},
		{"écran", "10x10", false},
		{"", "10x10", false},
	} {
		t.Run(tc.name+"/"+tc.mode, func(t *testing.T) {
			c, warnings := parseString(t, "output."+tc.name+" = virtual "+tc.mode+"\n")
			if !tc.valid {
				if len(warnings) != 1 || len(c.Outputs) != 0 {
					t.Fatalf("outputs=%+v warnings=%v", c.Outputs, warnings)
				}
				return
			}
			if len(warnings) != 0 || len(c.Outputs) != 1 {
				t.Fatalf("outputs=%+v warnings=%v", c.Outputs, warnings)
			}
			o := c.Outputs[0]
			if !o.Virtual || o.Mode != tc.mode || o.Name != tc.name || o.Off || o.ScaleOnly {
				t.Fatalf("output=%+v", o)
			}
		})
	}
}

func TestVirtualOutputHDRKeys(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "after"}[before], func(t *testing.T) {
			declaration := "output.tv = virtual 1920x1080\n"
			subkeys := "output.tv.hdr = on\noutput.tv.sdr-brightness = 300\noutput.tv.sdr-expand = always\noutput.tv.sdr-expand-intensity = 0.9\noutput.tv.sdr-expand-peak = 800\noutput.tv.sdr-decode = srgb\n"
			input := declaration + subkeys
			if before {
				input = subkeys + declaration
			}
			c, raw, warnings, err := parseBytes([]byte(input))
			if err != nil || len(warnings) != 0 {
				t.Fatalf("err=%v warnings=%v", err, warnings)
			}
			o := c.Outputs[0]
			if !o.Virtual || !o.HDR || o.SDRBrightness != 300 || o.SDRExpand != domain.SDRExpandAlways || o.SDRExpandIntensity != 0.9 || o.SDRExpandPeak != 800 || o.SDRDecode != domain.SDRDecodeSRGB {
				t.Fatalf("output=%+v", o)
			}
			if got := o.HDRSettings(); got != (domain.OutputHDRSettings{Enabled: true, SDRBrightness: 300, ExpansionPeakNits: 800}) {
				t.Fatalf("settings=%+v", got)
			}
			for _, key := range []string{"hdr", "sdr-brightness", "sdr-expand", "sdr-expand-intensity", "sdr-expand-peak", "sdr-decode"} {
				if _, ok := raw["output.tv."+key]; !ok {
					t.Fatalf("%s dropped from effective config", key)
				}
			}
		})
	}
}

func TestVirtualOutputOverrides(t *testing.T) {
	for _, tc := range []struct {
		input    string
		virtual  bool
		mode     string
		warnings int
	}{
		{"output.tv = virtual 10x10\noutput.tv = virtual foo\n", true, "10x10", 1},
		{"output.tv = virtual 10x10\noutput.tv = preferred\noutput.tv.hdr = on\n", false, "", 1},
		{"output.tv.scale = 2\noutput.tv = virtual 10x10\n", true, "10x10", 0},
	} {
		c, warnings := parseString(t, tc.input)
		if len(warnings) != tc.warnings || len(c.Outputs) != 1 || c.Outputs[0].Virtual != tc.virtual || c.Outputs[0].Mode != tc.mode {
			t.Fatalf("outputs=%+v warnings=%v", c.Outputs, warnings)
		}
		if strings.Contains(tc.input, ".scale") && c.Outputs[0].Scale != 2 {
			t.Fatal("scale lost")
		}
	}
}
