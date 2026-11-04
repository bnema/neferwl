package domain

import "testing"

func TestParseBackend(t *testing.T) {
	for _, b := range []Backend{BackendDRM, BackendHeadless, BackendServer} {
		got, err := ParseBackend(string(b))
		if err != nil || got != b {
			t.Fatalf("ParseBackend(%q) = %q, %v", b, got, err)
		}
	}
	for _, s := range []string{"", "DRM", "x11"} {
		if _, err := ParseBackend(s); err == nil {
			t.Fatalf("ParseBackend(%q) succeeded", s)
		}
	}
}
