package domain

import (
	"strings"
	"testing"
)

func TestOutputInfoRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		info OutputInfo
		want MonitorRef
	}{
		{"empty", OutputInfo{}, MonitorRef{}},
		{"connector only", OutputInfo{Name: "DP-5", Make: "Dell", Serial: "X"}, MonitorRef{Connector: "DP-5"}},
		{"fingerprint", OutputInfo{Name: "DP-7", EDIDID: "10ac0000aaaa0000011e", EDIDHash: "h1"}, MonitorRef{Connector: "DP-7", EDIDID: "10ac0000aaaa0000011e", EDIDHash: "h1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Ref(); got != tc.want {
				t.Fatalf("Ref() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOutputInfoPhysical(t *testing.T) {
	for _, tc := range []struct {
		name string
		info OutputInfo
		want bool
	}{
		{"physical", OutputInfo{Name: "DP-1"}, true},
		{"physical with id", OutputInfo{Name: "DP-1", EDIDID: "aa"}, true},
		{"virtual", OutputInfo{Name: "HEADLESS-1", Virtual: true}, false},
		{"virtual with id", OutputInfo{Name: "ext", Virtual: true, EDIDID: "aa"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Physical(); got != tc.want {
				t.Fatalf("Physical() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestValidEDIDID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"", false},
		{"a", true},
		{"10ac0000aaaa0000011e", true},
		{"Az09._-", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"a b", false},
		{"a/b", false},
		{"é", false},
		{"a\n", false},
	} {
		if got := ValidEDIDID(tc.id); got != tc.want {
			t.Errorf("ValidEDIDID(%q) = %t, want %t", tc.id, got, tc.want)
		}
	}
}
