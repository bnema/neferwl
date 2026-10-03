package ports

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

func TestBufferTransformInvert(t *testing.T) {
	const w, h = 7.0, 3.0
	for tr := BufferTransform(0); tr < 8; tr++ {
		assert.Equal(t, tr, tr.Invert().Invert(), "transform %d", tr)
		bw, bh := w, h
		if tr.Rotated() {
			bw, bh = h, w
		}
		for _, p := range [][2]float64{{0, 0}, {1.5, 1}, {6.25, 2.5}} {
			bx, by := tr.ToBuffer(p[0], p[1], w, h)
			x, y := tr.Invert().ToBuffer(bx, by, bw, bh)
			assert.InDelta(t, p[0], x, 1e-9, "transform %d point %v", tr, p)
			assert.InDelta(t, p[1], y, 1e-9, "transform %d point %v", tr, p)
		}
	}
}

// RectToBuffer is the bounding box of the rect's corners under ToBuffer.
func TestRectToBuffer(t *testing.T) {
	const w, h = 7, 3
	r := image.Rect(1, 0, 3, 2)
	assert.Equal(t, image.Rect(0, 5, 1, 7), BufferTransform(1).RectToBuffer(image.Rect(0, 0, 2, 1), w, h))
	for tr := BufferTransform(0); tr < 8; tr++ {
		got := tr.RectToBuffer(r, w, h)
		x0, y0 := tr.ToBuffer(float64(r.Min.X), float64(r.Min.Y), w, h)
		x1, y1 := tr.ToBuffer(float64(r.Max.X), float64(r.Max.Y), w, h)
		x2, y2 := tr.ToBuffer(float64(r.Min.X), float64(r.Max.Y), w, h)
		x3, y3 := tr.ToBuffer(float64(r.Max.X), float64(r.Min.Y), w, h)
		want := image.Rect(int(min(x0, x1, x2, x3)), int(min(y0, y1, y2, y3)), int(max(x0, x1, x2, x3)), int(max(y0, y1, y2, y3)))
		assert.Equal(t, want, got, "transform %d", tr)
		assert.Equal(t, r.Dx()*r.Dy(), got.Dx()*got.Dy(), "transform %d area", tr)
	}
}

func TestBufferTransformSize(t *testing.T) {
	for tr := BufferTransform(0); tr < 8; tr++ {
		w, h := tr.Size(7, 3)
		if tr.Rotated() {
			assert.Equal(t, [2]int{3, 7}, [2]int{w, h}, "transform %d", tr)
		} else {
			assert.Equal(t, [2]int{7, 3}, [2]int{w, h}, "transform %d", tr)
		}
	}
}

func TestOutputPlacementToTarget(t *testing.T) {
	o := OutputPlacement{Info: OutputInfo{Width: 2560, Height: 1440}, Scale: 1, X: 100, Transform: 1}
	x, y := o.ToTarget(100, 0)
	wx, wy := BufferTransform(1).ToBuffer(0, 0, 1440, 2560)
	assert.Equal(t, [2]float64{wx, wy}, [2]float64{x, y})
	assert.Equal(t, [2]float64{0, 1440}, [2]float64{x, y})

	o.Transform = 0
	x, y = o.ToTarget(150, 20)
	assert.Equal(t, [2]float64{50, 20}, [2]float64{x, y})
}
