package domain

import (
	"slices"
	"testing"
)

func TestCastFormats(t *testing.T) {
	for _, tc := range []struct {
		hdr  bool
		want []uint32
	}{
		{false, []uint32{CastXRGB8888, CastARGB8888}},
		{true, []uint32{CastXBGR2101010, CastXRGB8888, CastARGB8888}},
	} {
		if got := CastFormats(tc.hdr); !slices.Equal(got, tc.want) {
			t.Errorf("CastFormats(%v) = %#x, want %#x", tc.hdr, got, tc.want)
		}
	}
	if testing.AllocsPerRun(10, func() { _ = CastFormats(true) }) != 0 {
		t.Fatal("CastFormats allocates")
	}
}
