package domain

import "testing"

func TestSurfaceColorPlainSDR(t *testing.T) {
	for _, tc := range []struct {
		name  string
		color SurfaceColor
		want  bool
	}{
		{"legacy", SurfaceColor{}, true},
		{"metadata only", SurfaceColor{Coefficients: 2, Range: 2, Chroma: 1, MaxCLL: 604}, true},
		{"transfer only", SurfaceColor{TF: ColorTFSRGB}, false},
		{"primaries only", SurfaceColor{Primaries: ColorPrimariesSRGB}, false},
		{"described sRGB", SurfaceColor{TF: ColorTFSRGB, Primaries: ColorPrimariesSRGB}, false},
		{"PQ", SurfaceColor{TF: ColorTFPQ, Primaries: ColorPrimariesBT2020}, false},
		{"extended linear", SurfaceColor{TF: ColorTFExtendedLinear, Primaries: ColorPrimariesSRGB}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.color.PlainSDR(); got != tc.want {
				t.Fatalf("PlainSDR()=%v want %v", got, tc.want)
			}
		})
	}
}
