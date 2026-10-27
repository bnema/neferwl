package domain

import (
	"os"
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

// ContentBatch is the window contents wayland hands to one output: at most
// one per window, the latest published. A content superseded before the
// output took the batch is overwritten in place; contents carry the window's
// Seq, so the release accounting needs only the newest one.
//
// Ownership moves with the batch: the sender must not touch it after the
// send, and the receiver resets it and gives it back through ContentReturn.
// Items is a slice, not a map: SurfaceContent is large, and a map would
// allocate its elements again after every clear.
type ContentBatch struct {
	Items []SurfaceContent
}

// Reset empties the batch, dropping its references to buffers, and keeps its
// capacity for reuse.
func (b *ContentBatch) Reset() {
	clear(b.Items)
	b.Items = b.Items[:0]
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
