package ports

import (
	"image"
	"math"
	"os"
)

// SurfaceColor is the committed encoding of a surface. Zero means sRGB.
// PQ+BT.2020 and extended-linear sRGB are color-managed; zero is legacy
// sRGB. MaxCLL/MaxFALL are in nits. YUV metadata is per surface commit.
type SurfaceColor struct {
	TF, Primaries   uint8
	MaxCLL, MaxFALL uint16
	// Coefficients/range follow wp_color_representation_v1 (zero =
	// unspecified, for YUV interpreted as BT.709 limited). Chroma is a
	// H.273 Chroma420SampleLocType + 1 (zero defaults to type 0).
	Coefficients, Range, Chroma uint8
}

const (
	ColorTFPQ             uint8 = 11 // wp_color_manager_v1 ST2084 PQ
	ColorTFExtendedLinear uint8 = 5  // wp_color_manager_v1 ext_linear
	ColorPrimariesBT2020  uint8 = 6  // wp_color_manager_v1 BT.2020
	ColorPrimariesSRGB    uint8 = 1
	ColorTFSRGB           uint8 = 9 // wp_color_manager_v1 sRGB
)

func (c SurfaceColor) IsPQ2020() bool {
	return c.TF == ColorTFPQ && c.Primaries == ColorPrimariesBT2020
}

func (c SurfaceColor) IsExtendedLinear() bool {
	return c.TF == ColorTFExtendedLinear && c.Primaries == ColorPrimariesSRGB
}

// ContentType is the committed wp_content_type_v1 hint.
type ContentType uint8

const (
	ContentNone ContentType = iota
	ContentPhoto
	ContentVideo
	ContentGame
)

// SurfaceContent carries wayland → output the latest committed content of a
// window: SHM, a client shared-memory buffer, DMABuf, a GPU buffer, or
// Solid, a single-pixel buffer. Renderers read them in place and never modify
// them. Empty means the window
// has no content. LogicalW and LogicalH are the surface size in logical
// pixels (buffer scale and viewport applied). Subsurfaces come in Children;
// Geometry is the part of the surface that is the window (xdg window
// geometry), the rest being client shadows.
type SurfaceContent struct {
	ID          WindowID
	ContentType ContentType
	// Surface is a never-reused wl_surface identity; Version identifies its
	// applied buffer content independently of the window publication Seq.
	Surface, Version uint64
	// Seq counts the window's contents (wayland sets it), for release.
	Seq                uint64
	Width, Height      int
	LogicalW, LogicalH int
	// Source is the committed crop in buffer pixels: x, y, width, height.
	// Zero width means the entire buffer is used.
	Source [4]float32
	// Transform is the wl_output.transform the client applied to the
	// buffer: readers apply its inverse. Width and Height stay the buffer's.
	Transform BufferTransform
	// Opaque ignores the alpha byte: an x format, or an opaque region that
	// covers the whole surface. It is false while Fade is set.
	Opaque bool
	// OpaqueRect is the largest rect of the opaque region inside a surface
	// that is not wholly Opaque, in logical pixels from the surface origin:
	// the pixels there ignore alpha too, so the surfaces below it are hidden
	// by it. Empty when there is none or while Fade is set.
	OpaqueRect Rect
	// Fade is how much wp_alpha_modifier_v1 fades the surface: 0 none,
	// 1 invisible. Its opacity is multiplied by 1-Fade.
	Fade   float32
	Color  SurfaceColor
	SHM    *SHMBuffer
	DMABuf *DMABuf
	// Solid is a single-pixel buffer (Width and Height are 1): the surface is
	// one flat color.
	Solid *SolidColor
	// Children are the subsurfaces, bottom to top, flattened.
	Children []Subsurface
	// Geometry is in logical pixels from the surface origin; empty means
	// the whole surface.
	Geometry Rect
	// Async asks for tearing presentation (wp_tearing_control_v1); outputs
	// honour it only in direct scanout.
	Async bool
	// Acquire is the explicit-sync fence of this content's root buffer
	// (wp_linux_drm_syncobj_v1): readers wait on it before reading the
	// buffer, instead of the buffer's implicit fences. Wayland owns it and
	// closes it when the buffer is released; a reader that keeps it past
	// the call duplicates it under Acquire.SyscallConn. nil: implicit sync.
	// Wayland closes it only after every output reported a later content of
	// the window as seen, so a reader never gets it already closed.
	Acquire *os.File
	// DamageHistory is what changed in the root surface's buffer over the
	// window's last contents, oldest first, ending with this one (Seq).
	// A renderer holding an older content redraws the union of the
	// entries after it, or everything when the history does not reach it.
	DamageHistory []SeqDamage
}

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

// DamageSince is the union of changes after content seq up to this one,
// and false when the history does not reach back to seq (redraw all).
func (c SurfaceContent) DamageSince(seq uint64) ([]Rect, bool) {
	out, ok := c.AppendDamageSince(nil, seq)
	if !ok {
		return nil, false
	}
	return out, true
}

// AppendDamageSince is DamageSince appending to dst, so a caller reusing
// its slice allocates nothing. When the history does not reach back to seq
// it returns dst unchanged and false.
func (c SurfaceContent) AppendDamageSince(dst []Rect, seq uint64) ([]Rect, bool) {
	if seq >= c.Seq {
		return dst, true
	}
	h := c.DamageHistory
	if len(h) == 0 || h[0].Seq > seq+1 || h[len(h)-1].Seq != c.Seq {
		return dst, false
	}
	n := len(dst)
	for _, d := range h {
		if d.Seq <= seq {
			continue
		}
		if d.Full {
			return dst[:n], false
		}
		dst = append(dst, d.Rects...)
	}
	return dst, true
}

// Subsurface is a child surface at X, Y logical pixels from the root
// surface origin. Below children are drawn under the root surface.
type Subsurface struct {
	X, Y  int
	Below bool
	SurfaceContent
}

// HasBuffer reports whether the surface itself carries a client buffer.
func (c SurfaceContent) HasBuffer() bool {
	return c.SHM != nil || c.DMABuf != nil || c.Solid != nil
}

// SolidColor is the color of a single-pixel buffer: premultiplied by alpha,
// 0 to 1, sRGB-encoded like the pixels of client buffers.
type SolidColor struct{ R, G, B, A float32 }

// Empty reports whether the content has nothing to draw.
func (c SurfaceContent) Empty() bool {
	return !c.HasBuffer() && len(c.Children) == 0
}

// SHMBuffer is a client wl_shm buffer: B8G8R8A8 pixels (argb8888/xrgb8888
// little-endian) at Offset in the pool File, Stride bytes per row. The
// client keeps writing the file, so renderers copy from it when they draw.
// Pool is unique per wl_shm pool for the session: renderers map a pool
// once. File stays open while the client's buffer exists and follows the
// DMABuf rules: duplicate it under File.SyscallConn before keeping it. A
// client may shrink the file at any time, so readers must survive faults.
type SHMBuffer struct {
	Pool           uint64
	File           *os.File
	Offset, Stride int
}

// DMABuf is a client GPU buffer (linux-dmabuf). Its files stay open while
// the client's buffer exists; a renderer that keeps it must duplicate the
// descriptors under File.SyscallConn so a concurrent close cannot hand it a
// reused one. ID is unique per buffer for the whole session: renderers key
// their imports on it.
type DMABuf struct {
	ID            uint64
	Width, Height int
	Format        uint32 // DRM fourcc
	Modifier      uint64
	Planes        []DMABufPlane
}

// DMABufPlane is one plane of a DMABuf.
type DMABufPlane struct {
	File           *os.File
	Offset, Stride uint32
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
