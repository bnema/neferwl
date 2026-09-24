package drm

import (
	"fmt"
	"sync"
	"unsafe"
)

// Hardware cursor through the legacy cursor ioctls: the GPU overlays a small
// buffer at a position, so moving it never re-renders the frame.

const (
	ioctlGetCap  = 0xC010640C // DRM_IOWR('d', 0x0C, struct drm_get_cap)
	ioctlCursor2 = 0xC02464BB // DRM_IOWR('d', 0xBB, struct drm_mode_cursor2)
	capCursorW   = 0x8
	capCursorH   = 0x9
	cursorBO     = 0x01
	cursorMove   = 0x02
)

type getCap struct{ capability, value uint64 }

type modeCursor2 struct {
	flags, crtcID uint32
	x, y          int32
	width, height uint32
	handle        uint32
	hotX, hotY    int32
}

// Cursor is the hardware cursor of one output. Move is safe to call from the
// input goroutine while the output goroutine changes the image.
type Cursor struct {
	mu         sync.Mutex
	fd         int
	crtc       uint32
	buf        *dumbBuffer
	size       int // buffer side
	hotX, hotY int
	x, y       int // physical position of the hotspot
	shown      bool
}

// newCursor allocates a cursor buffer at the driver's preferred size (64 when
// the driver does not say).
func newCursor(fd int, crtc uint32) (*Cursor, error) {
	// The plane is square: the smaller of the width and height caps.
	size := 0
	for _, c := range []uint64{capCursorW, capCursorH} {
		v := getCap{capability: c}
		if ioctl(fd, ioctlGetCap, unsafe.Pointer(&v)) == nil && v.value >= 16 && v.value <= 512 {
			if size == 0 || int(v.value) < size {
				size = int(v.value)
			}
		}
	}
	if size == 0 {
		size = 64
	}
	buf, err := newDumb(fd, size, size)
	if err != nil {
		return nil, err
	}
	// The cursor ioctl passes no pitch: drivers assume width*4.
	if int(buf.pitch) != size*4 {
		buf.destroy(fd)
		return nil, fmt.Errorf("cursor buffer pitch %d, want %d", buf.pitch, size*4)
	}
	return &Cursor{fd: fd, crtc: crtc, buf: buf, size: size}, nil
}

// Limit is the largest image side the cursor plane accepts.
func (c *Cursor) Limit() int { return c.size }

// SetImage shows premultiplied ARGB8888 pixels (w×h, w*4 per row) with the
// click point at (hotX, hotY). Larger images are cropped to the plane.
func (c *Cursor) SetImage(pixels []byte, w, h, hotX, hotY int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf == nil {
		return nil
	}
	pitch := int(c.buf.pitch)
	clear(c.buf.mem)
	for y := range min(h, c.size) {
		row := pixels[y*w*4:]
		copy(c.buf.mem[y*pitch:y*pitch+min(w, c.size)*4], row[:min(w, c.size)*4])
	}
	c.hotX, c.hotY, c.shown = hotX, hotY, true
	return c.apply()
}

// apply (re)attaches the buffer at the current position.
func (c *Cursor) apply() error {
	if !c.shown || c.buf == nil {
		return nil
	}
	v := modeCursor2{flags: cursorBO | cursorMove, crtcID: c.crtc, x: int32(c.x - c.hotX), y: int32(c.y - c.hotY), width: uint32(c.size), height: uint32(c.size), handle: c.buf.handle, hotX: int32(c.hotX), hotY: int32(c.hotY)}
	return ioctl(c.fd, ioctlCursor2, unsafe.Pointer(&v))
}

// Move places the hotspot at physical (x, y). Errors while another session
// owns the card (VT switched away) are ignored; Reapply restores the cursor.
func (c *Cursor) Move(x, y float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.x, c.y = int(x), int(y)
	if !c.shown || c.buf == nil {
		return
	}
	v := modeCursor2{flags: cursorMove, crtcID: c.crtc, x: int32(c.x - c.hotX), y: int32(c.y - c.hotY)}
	_ = ioctl(c.fd, ioctlCursor2, unsafe.Pointer(&v))
}

// Reapply shows the cursor again after a modeset (VT switch back).
func (c *Cursor) Reapply() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apply()
}

// close hides the cursor and frees its buffer.
func (c *Cursor) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf == nil {
		return
	}
	// Best effort: at exit the card may already belong to another session.
	v := modeCursor2{flags: cursorBO, crtcID: c.crtc}
	_ = ioctl(c.fd, ioctlCursor2, unsafe.Pointer(&v))
	c.buf.destroy(c.fd)
	c.buf, c.shown = nil, false
}
