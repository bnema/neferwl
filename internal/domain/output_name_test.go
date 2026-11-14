package domain

import "testing"

func TestValidOutputName(t *testing.T) {
	for _, name := range []string{"DP-1", "TV-1_test.2", "a", "...", ".hidden", "_"} {
		if !ValidOutputName(name) {
			t.Errorf("rejected valid name %q", name)
		}
	}
	for _, name := range []string{"", ".", "..", "bad/name", `bad\name`, "two words", "tab\t", "line\n", "nul\x00", "écran", "a:b"} {
		if ValidOutputName(name) {
			t.Errorf("accepted invalid name %q", name)
		}
	}
}
