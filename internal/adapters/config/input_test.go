package config

import (
	"slices"
	"testing"
)

func TestOutputInputs(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []string
		warnings    int
	}{
		{"single", "output.tv.input = Mouse passthrough*\n", []string{"Mouse passthrough*"}, 0},
		{"repeated", "output.tv.input = Mouse*\noutput.tv.input = Tablet ?\n", []string{"Mouse*", "Tablet ?"}, 0},
		{"comma is literal", "output.tv.input = Mouse [a,b]*\n", []string{"Mouse [a,b]*"}, 0},
		{"before declaration", "output.tv.input = Mouse*\noutput.tv = virtual 10x10\n", []string{"Mouse*"}, 0},
		{"after declaration", "output.tv = virtual 10x10\noutput.tv.input = Mouse*\n", []string{"Mouse*"}, 0},
		{"physical", "output.tv.input = Mouse*\noutput.tv = preferred\n", []string{"Mouse*"}, 0},
		{"bad bracket", "output.tv.input = [\n", nil, 1},
		{"bad escape", "output.tv.input = Mouse\\\n", nil, 1},
		{"empty", "output.tv.input =\n", nil, 1},
		{"invalid preserves earlier", "output.tv.input = Mouse*\noutput.tv.input = [\noutput.tv.input = Tablet*\n", []string{"Mouse*", "Tablet*"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, warnings := parseString(t, tc.input)
			if len(warnings) != tc.warnings {
				t.Fatal(warnings)
			}
			if tc.want == nil {
				if len(c.Outputs) != 0 {
					t.Fatal(c.Outputs)
				}
				return
			}
			if len(c.Outputs) != 1 || !slices.Equal(c.Outputs[0].Inputs, tc.want) {
				t.Fatal(c.Outputs)
			}
		})
	}
}
