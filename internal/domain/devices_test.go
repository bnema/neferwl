package domain

import "testing"

func TestMultiGPUMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want MultiGPUMode
	}{{"", MultiGPUAuto}, {"auto", MultiGPUAuto}, {"direct", MultiGPUDirect}, {"off", MultiGPUOff}} {
		if got := ParseMultiGPUMode(tc.in); got != tc.want {
			t.Fatalf("%q: %v want %v", tc.in, got, tc.want)
		}
	}
}
