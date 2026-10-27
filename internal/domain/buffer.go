package domain

import (
	"image"
	"math"
)

// BufferTransform is a wl_output.transform value: bit 0 rotates 90°
// counter-clockwise, bit 1 rotates 180°, bit 2 flips around the vertical
// axis first.
type BufferTransform uint8

// Rotated reports whether width and height swap between buffer and surface.
func (t BufferTransform) Rotated() bool { return t&1 != 0 }

// Size returns the size of a w×h space seen through t: width and height swap
// when t is Rotated.
func (t BufferTransform) Size(w, h int) (int, int) {
	if t.Rotated() {
		return h, w
	}
	return w, h
}

// ToBuffer maps a point of a w×h surface (logical, before scale) to the
// same point of its buffer, in the buffer's untransformed w'×h' axes, where
// w' and h' swap when the transform is Rotated.
func (t BufferTransform) ToBuffer(x, y, w, h float64) (float64, float64) {
	if t&4 != 0 {
		x = w - x
	}
	switch t & 3 {
	case 1:
		return y, w - x
	case 2:
		return w - x, h - y
	case 3:
		return h - y, x
	}
	return x, y
}

// Invert is the transform that maps target points back: for a w×h scene and
// its w'×h' target, t.Invert().ToBuffer(t.ToBuffer(p, w, h), w', h') == p.
func (t BufferTransform) Invert() BufferTransform {
	if t&1 != 0 && t&4 == 0 {
		return t ^ 2
	}
	return t
}

// RectToBuffer maps a rectangle of a w×h surface to the buffer. Integer
// rectangles stay integer under these transforms; rounding absorbs float noise.
func (t BufferTransform) RectToBuffer(r image.Rectangle, w, h int) image.Rectangle {
	ax, ay := t.ToBuffer(float64(r.Min.X), float64(r.Min.Y), float64(w), float64(h))
	bx, by := t.ToBuffer(float64(r.Max.X), float64(r.Max.Y), float64(w), float64(h))
	return image.Rect(int(math.Round(ax)), int(math.Round(ay)), int(math.Round(bx)), int(math.Round(by)))
}

// SeqDamage is what content Seq changed from the one before: Rects in
// root buffer pixels, or everything when Full.
type SeqDamage struct {
	Seq   uint64
	Full  bool
	Rects []Rect
}

// DMABufFormat is a DRM fourcc with a modifier the renderer can import.
type DMABufFormat struct {
	Format   uint32
	Modifier uint64
}

// DMABufSupport is what the renderer imports: the render device (a dev_t,
// 0 when unknown) and the formats. No formats means no dmabuf.
type DMABufSupport struct {
	Device  uint64
	Formats []DMABufFormat
}
