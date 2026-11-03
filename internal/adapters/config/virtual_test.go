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

func TestVirtualOutputSDRWarnings(t *testing.T) {
	for _, field := range []struct{ key, value string }{
		{"hdr", "on"}, {"sdr-expand", "always"}, {"sdr-expand-intensity", "0.9"},
		{"sdr-expand-peak", "800"}, {"sdr-decode", "srgb"},
	} {
		for _, before := range []bool{true, false} {
			order := "after"
			if before {
				order = "before"
			}
			t.Run(field.key+"/"+order, func(t *testing.T) {
				declaration := "output.tv = virtual 1920x1080\n"
				subkey := "output.tv." + field.key + " = " + field.value + "\n"
				input, line := declaration+subkey, 2
				if before {
					input, line = subkey+declaration, 1
				}
				c, raw, warnings, err := parseBytes([]byte(input))
				if err != nil || len(warnings) != 1 || warnings[0].Line != line || !strings.Contains(warnings[0].Msg, "ignored on virtual output") {
					t.Fatalf("err=%v warnings=%v", err, warnings)
				}
				o := c.Outputs[0]
				if !o.Virtual || o.HDR || o.SDRExpand != domain.SDRExpandAuto || o.SDRExpandIntensity != domain.DefaultSDRExpandIntensity || o.SDRExpandPeak != 0 || o.SDRDecode != domain.SDRDecodeGamma22 {
					t.Fatalf("output=%+v", o)
				}
				if _, ok := raw["output.tv."+field.key]; ok {
					t.Fatal("ignored key retained in effective config")
				}
			})
		}
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
