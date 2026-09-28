package ports

import "testing"

// ToBuffer matches wl_output.transform: the buffer holds the surface rotated
// counter-clockwise by the transform (after a flip around the vertical axis
// for flipped values). Checked on the surface's top-left corner of a 3×2
// surface.
func TestBufferTransformToBuffer(t *testing.T) {
	for _, tc := range []struct {
		t          BufferTransform
		rotated    bool
		bx, by     float64 // buffer point of surface (0.5, 0.5)
		bufW, bufH int
	}{
		{0, false, 0.5, 0.5, 3, 2},
		{1, true, 0.5, 2.5, 2, 3},
		{2, false, 2.5, 1.5, 3, 2},
		{3, true, 1.5, 0.5, 2, 3},
		{4, false, 2.5, 0.5, 3, 2},
		{5, true, 0.5, 0.5, 2, 3},
		{6, false, 0.5, 1.5, 3, 2},
		{7, true, 1.5, 2.5, 2, 3},
	} {
		if tc.t.Rotated() != tc.rotated {
			t.Errorf("%d: rotated %v", tc.t, tc.t.Rotated())
		}
		x, y := tc.t.ToBuffer(0.5, 0.5, 3, 2)
		if x != tc.bx || y != tc.by {
			t.Errorf("%d: (0.5,0.5) -> (%v,%v), want (%v,%v)", tc.t, x, y, tc.bx, tc.by)
		}
		if x < 0 || y < 0 || x > float64(tc.bufW) || y > float64(tc.bufH) {
			t.Errorf("%d: (%v,%v) outside the %dx%d buffer", tc.t, x, y, tc.bufW, tc.bufH)
		}
	}
}
