package drm

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// Hardware cursor through the legacy cursor ioctls: the GPU overlays a small
// buffer at a position, so moving it never re-renders the frame.

const (
	ioctlGetCap  = 0xC010640C // DRM_IOWR('d', 0x0C, struct drm_get_cap)
	ioctlCursor2 = 0xC02464BB // DRM_IOWR('d', 0xBB, struct drm_mode_cursor2)
	ioctlVblank  = 0xC018643A // DRM_IOWR('d', 0x3A, union drm_wait_vblank)
	vblankRel    = 0x1        // _DRM_VBLANK_RELATIVE
	vblankCrtcSh = 1          // _DRM_VBLANK_HIGH_CRTC_SHIFT
	vblankCrtcMk = 0x3e       // _DRM_VBLANK_HIGH_CRTC_MASK
	capCursorW   = 0x8
	capCursorH   = 0x9
	cursorBO     = 0x01
	cursorMove   = 0x02
)

type getCap struct{ capability, value uint64 }

// waitVblank is union drm_wait_vblank (request and reply share 24 bytes).
type waitVblank struct {
	typ, sequence uint32
	sec, usec     int64
}

// cursorPlane is the kernel side of the hardware cursor.
type cursorPlane interface {
	// Set issues DRM_IOCTL_MODE_CURSOR2.
	Set(v *modeCursor2) error
	// WaitVblank blocks until the next vblank of the cursor's CRTC.
	WaitVblank() error
}

// kmsCursorPlane drives the cursor of the CRTC at index pipe.
type kmsCursorPlane struct{ fd, pipe int }

func (p kmsCursorPlane) Set(v *modeCursor2) error {
	return ioctl(p.fd, ioctlCursor2, unsafe.Pointer(v))
}

func (p kmsCursorPlane) WaitVblank() error {
	v := waitVblank{typ: vblankRel | uint32(p.pipe<<vblankCrtcSh)&vblankCrtcMk, sequence: 1}
	return ioctl(p.fd, ioctlVblank, unsafe.Pointer(&v))
}

type modeCursor2 struct {
	flags, crtcID uint32
	x, y          int32
	width, height uint32
	handle        uint32
	hotX, hotY    int32
}

// Cursor is the hardware cursor of one output. Move is safe to call from the
// input goroutine while the output goroutine changes the image.
//
// Move only records the position. A worker waits for the next vblank and then
// applies the latest one, so the cursor moves at most once per frame, between
// scanouts: moving it mid-scanout tears it into doubled images, and input is
// never throttled by the ioctl.
type Cursor struct {
	io         sync.Mutex // serialises cursor ioctls and guards the fields below
	fd         int
	crtc       uint32
	buf        *dumbBuffer
	size       int // buffer side
	hotX, hotY int
	shown      bool
	hidden     bool // Hide was called; the next move shows it again
	plane      cursorPlane

	mu    sync.Mutex // guards x, y and stats; never held across an ioctl
	x, y  int        // physical position of the hotspot
	stats CursorStats

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
}

// CursorStats counts cursor work since the last TakeStats.
type CursorStats struct {
	Moves    int           // Move calls
	Ioctls   int           // move ioctls issued by the worker
	MaxIoctl time.Duration // slowest move ioctl
}

// TakeStats returns and resets the counters.
func (c *Cursor) TakeStats() CursorStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	c.stats = CursorStats{}
	return s
}

// newCursor allocates a cursor buffer at the driver's preferred size (64 when
// the driver does not say).
func newCursor(fd int, crtc uint32, pipe int) (*Cursor, error) {
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
	c := &Cursor{fd: fd, crtc: crtc, buf: buf, size: size, plane: kmsCursorPlane{fd: fd, pipe: pipe}}
	c.start()
	return c, nil
}

// start runs the worker that applies coalesced moves.
func (c *Cursor) start() {
	c.wake = make(chan struct{}, 1)
	c.stop = make(chan struct{})
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		for {
			select {
			case <-c.stop:
				return
			case <-c.wake:
				// Errors (VT switched away) fall through: the move
				// fails too and Reapply restores the cursor.
				_ = c.plane.WaitVblank()
				c.io.Lock()
				if c.hidden {
					// A move shows it again, even before its first image.
					c.hidden = false
					_ = c.apply()
				} else if c.shown && c.buf != nil {
					x, y := c.position()
					v := modeCursor2{flags: cursorMove, crtcID: c.crtc, x: int32(x - c.hotX), y: int32(y - c.hotY)}
					// Errors while another session owns the card (VT switched
					// away) are ignored; Reapply restores the cursor.
					start := time.Now()
					_ = c.plane.Set(&v)
					d := time.Since(start)
					c.mu.Lock()
					c.stats.Ioctls++
					c.stats.MaxIoctl = max(c.stats.MaxIoctl, d)
					c.mu.Unlock()
				}
				c.io.Unlock()
			}
		}
	}()
}

func (c *Cursor) position() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.x, c.y
}

// Limit is the largest image side the cursor plane accepts.
func (c *Cursor) Limit() int { return c.size }

// SetImage shows premultiplied ARGB8888 pixels (w×h, w*4 per row) with the
// click point at (hotX, hotY). Larger images are cropped to the plane.
func (c *Cursor) SetImage(pixels []byte, w, h, hotX, hotY int) error {
	c.io.Lock()
	defer c.io.Unlock()
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

// apply (re)attaches the buffer at the current position. Callers hold c.io.
func (c *Cursor) apply() error {
	if !c.shown || c.hidden || c.buf == nil {
		return nil
	}
	x, y := c.position()
	v := modeCursor2{flags: cursorBO | cursorMove, crtcID: c.crtc, x: int32(x - c.hotX), y: int32(y - c.hotY), width: uint32(c.size), height: uint32(c.size), handle: c.buf.handle, hotX: int32(c.hotX), hotY: int32(c.hotY)}
	return c.plane.Set(&v)
}

// Move places the hotspot at physical (x, y). It never blocks on the GPU:
// the worker applies the latest position.
func (c *Cursor) Move(x, y float64) {
	c.mu.Lock()
	c.x, c.y = int(x), int(y)
	c.stats.Moves++
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default: // a move is already queued; it will read this position
	}
}

// Hide removes the cursor from the output until the next move (the pointer
// is on another output).
func (c *Cursor) Hide() {
	c.io.Lock()
	defer c.io.Unlock()
	if c.buf == nil || c.hidden {
		return
	}
	c.hidden = true
	v := modeCursor2{flags: cursorBO, crtcID: c.crtc}
	_ = c.plane.Set(&v)
}

// Reapply shows the cursor again after a modeset (VT switch back).
func (c *Cursor) Reapply() error {
	c.io.Lock()
	defer c.io.Unlock()
	return c.apply()
}

// close stops the worker, hides the cursor and frees its buffer.
func (c *Cursor) close() {
	if c.stop != nil {
		close(c.stop)
		<-c.done
		c.stop = nil
	}
	c.io.Lock()
	defer c.io.Unlock()
	if c.buf == nil {
		return
	}
	// Best effort: at exit the card may already belong to another session.
	v := modeCursor2{flags: cursorBO, crtcID: c.crtc}
	_ = c.plane.Set(&v)
	c.buf.destroy(c.fd)
	c.buf, c.shown = nil, false
}
