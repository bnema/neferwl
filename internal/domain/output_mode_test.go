package domain

import "testing"

func TestParseOutputMode(t *testing.T) {
	for _, tc := range []struct {
		value   string
		valid   bool
		refresh int
	}{
		{"10x10@0.0001", false, 0}, {"10x10@2147483.648", false, 0}, {"10x10@2147483.647", true, 2147483647}, {"10x10@0.0005", true, 1},
		{"10x10", true, 60000}, {"10x10@59.94", true, 59940}, {"preferred", false, 0}, {"0x10", false, 0}, {"16385x10", false, 0},
		{"10x10@NaN", false, 0}, {"10x10@1e100", false, 0}, {"10x10@", false, 0}, {"-1x10", false, 0}, {"16384x16384@120", true, 120000},
	} {
		t.Run(tc.value, func(t *testing.T) {
			mode, err := ParseOutputMode(tc.value)
			if (err == nil) != tc.valid || tc.valid && mode.RefreshMilli != tc.refresh {
				t.Fatalf("mode=%+v err=%v", mode, err)
			}
			if !tc.valid {
				return
			}
			// The formatter round-trips exactly, without float rounding.
			back, err := ParseOutputMode(FormatOutputMode(mode))
			if err != nil || back != mode {
				t.Fatalf("%q -> %q -> %+v %v", tc.value, FormatOutputMode(mode), back, err)
			}
		})
	}
	if got := FormatOutputMode(OutputMode{Width: 640, Height: 480, RefreshMilli: 59940}); got != "640x480@59.940" {
		t.Fatal(got)
	}
}
