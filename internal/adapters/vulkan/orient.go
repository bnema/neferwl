package vulkan

import (
	"image"

	"github.com/bnema/neferwl/internal/ports"
)

// sceneSize is the size of a scene in scene-physical pixels: the target's,
// swapped when the output transform t rotates by 90 or 270 degrees.
func (r *Renderer) sceneSize(t ports.BufferTransform) (int, int) {
	return t.Size(r.width, r.height)
}

// orient maps draws built in scene space to the target, in place and without
// allocating: the target holds the scene transformed by t, like a client
// buffer holds its surface under wl_surface.set_buffer_transform. Each rect
// goes through t.RectToBuffer (integer rects stay integer under these eight
// isometries, so the rounding is exact) and each source map is composed with
// the affine target-to-scene map, the inverse transform. The vertex shader's
// target size (misc[2:4]) stays the target's.
//
// Pixel centres map to pixel centres under these isometries, so a draw
// flagged flagExact still reads texelFetch(floor(src)) on the right texel.
//
// It runs once per frame, after damage clipping, and before any consumer of
// the target: HDR composition (hdrOwn is target-sized and recordHDR reads the
// already-oriented target) and the capture and Pixels paths stay unchanged.
func (r *Renderer) orient(ds []draw, t ports.BufferTransform) {
	if t == 0 {
		return
	}
	sw, sh := r.sceneSize(t)
	inv := t.Invert()
	tw, th := float64(r.width), float64(r.height)
	// scene = f0 + x*fx + y*fy for a target point (x, y).
	f0x, f0y := inv.ToBuffer(0, 0, tw, th)
	fxx, fxy := inv.ToBuffer(1, 0, tw, th)
	fyx, fyy := inv.ToBuffer(0, 1, tw, th)
	fxx, fxy = fxx-f0x, fxy-f0y
	fyx, fyy = fyx-f0x, fyy-f0y
	for i := range ds {
		pc := &ds[i].pc
		rect := t.RectToBuffer(image.Rect(int(pc.rect[0]), int(pc.rect[1]), int(pc.rect[2]), int(pc.rect[3])), sw, sh)
		pc.rect = [4]int32{int32(rect.Min.X), int32(rect.Min.Y), int32(rect.Max.X), int32(rect.Max.Y)}
		if pc.misc[0] == modeSolid {
			continue
		}
		m0x, m0y := float64(pc.mapv[0]), float64(pc.mapv[1])
		mxx, mxy := float64(pc.mapv[2]), float64(pc.mapv[3])
		myx, myy := float64(pc.mapy[0]), float64(pc.mapy[1])
		pc.mapv = [4]float32{
			float32(m0x + f0x*mxx + f0y*myx),
			float32(m0y + f0x*mxy + f0y*myy),
			float32(fxx*mxx + fxy*myx),
			float32(fxx*mxy + fxy*myy),
		}
		pc.mapy[0] = float32(fyx*mxx + fyy*myx)
		pc.mapy[1] = float32(fyx*mxy + fyy*myy)
	}
}
