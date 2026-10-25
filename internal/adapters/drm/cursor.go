package drm

import (
	"errors"
	"slices"
	"sync"

	"github.com/bnema/neferwl/internal/ports"
)

// Hardware cursor on the CRTC's cursor plane. Move and Hide come from the
// input goroutine: they only record the position and wake the output,
// whose goroutine puts the cursor plane in its next atomic commit. A move
// while a commit is pending waits for it, so the cursor moves at most once
// per flip and never blocks input.

const (
	ioctlGetCap = 0xC010640C // DRM_IOWR('d', 0x0C, struct drm_get_cap)
	capCursorW  = 0x8
	capCursorH  = 0x9
)

type getCap struct{ capability, value uint64 }

// Cursor is the hardware cursor of one output.
type Cursor struct {
	mu    sync.Mutex // guards x, y, away and moves
	x, y  int        // physical position of the hotspot
	away  bool       // Hide was called and no Move since
	moves int
	wake  chan struct{}

	// Owned by the output goroutine.
	plane *plane
	size  int
	// imgs are the two images (nil: none), alloc says who allocated them
	// (allocDumb or allocVulkan) and fallback makes the renderer's images
	// when KMS refuses the dumb ones. fbs mirrors their framebuffers; cur
	// is on screen or next.
	imgs       cursorImages
	alloc      string
	fallback   func() (cursorImages, error)
	fbs        [2]uint32
	cur        int
	image      bool // an image is loaded
	hotX, hotY int
	// last is a copy of the loaded image, written again into new images
	// when the allocator changes.
	last    laterImage
	applied cursorState // what the last commit put on the plane
	commits int
	// screen is the image the plane scans out (0: none); flight is what
	// the pending commit puts on it when flying. An image is written only
	// when it is neither, so the plane never shows a half-written image.
	screen uint32
	flight cursorState
	flying bool
	// later is an image that waits for a free slot (both on screen or in
	// flight); off is set when KMS refused the cursor plane.
	later *laterImage
	off   bool
}

type laterImage struct {
	pixels           []byte
	w, h, hotX, hotY int
}

// cursorState is the cursor plane as committed: off, or fb at x, y.
type cursorState struct {
	on   bool
	fb   uint32
	x, y int
}

// CursorStats counts cursor work since the last TakeStats.
type CursorStats struct {
	Moves   int // Move calls
	Commits int // commits that moved or changed the cursor
}

func newCursor(p *plane, size int) *Cursor {
	return &Cursor{plane: p, size: size, wake: make(chan struct{}, 1)}
}

// TakeStats returns and resets the counters. Output goroutine only.
func (c *Cursor) TakeStats() CursorStats {
	c.mu.Lock()
	s := CursorStats{Moves: c.moves, Commits: c.commits}
	c.moves, c.commits = 0, 0
	c.mu.Unlock()
	return s
}

// Limit is the largest image side the cursor plane accepts.
func (c *Cursor) Limit() int { return c.size }

// Move places the hotspot at physical (x, y). It never blocks.
func (c *Cursor) Move(x, y float64) {
	c.mu.Lock()
	c.x, c.y = int(x), int(y)
	c.away = false
	c.moves++
	c.mu.Unlock()
	c.poke()
}

// Hide removes the cursor from the output until the next move (the pointer
// is on another output).
func (c *Cursor) Hide() {
	c.mu.Lock()
	c.away = true
	c.mu.Unlock()
	c.poke()
}

func (c *Cursor) poke() {
	select {
	case c.wake <- struct{}{}:
	default: // a wake is queued; the output reads the latest state
	}
}

// setup allocates the two cursor images and their framebuffers: KMS dumb
// buffers first, then the renderer's images. dumbErr is why the dumb buffers
// were not used (nil when they are); err is set when no images exist.
func (c *Cursor) setup(k kms, r ports.Renderer) (dumbErr, err error) {
	c.fallback = func() (cursorImages, error) { return newRendererCursor(k, r, c.size) }
	d, dumbErr := newDumbCursor(k, c.size)
	if dumbErr == nil {
		c.install(d, allocDumb)
		return nil, nil
	}
	imgs, err := c.fallback()
	if err != nil {
		return dumbErr, err
	}
	c.install(imgs, allocVulkan)
	return dumbErr, nil
}

// install makes imgs the cursor images; none is on screen or in flight.
func (c *Cursor) install(imgs cursorImages, alloc string) {
	c.imgs, c.alloc = imgs, alloc
	for i := range c.fbs {
		c.fbs[i] = imgs.fb(i)
	}
	c.cur = 0
	c.screen, c.flight, c.flying, c.applied = 0, cursorState{}, false, cursorState{}
}

// useFallback replaces refused dumb images with the renderer's and writes
// the current image into them. It reports false when it cannot: no fallback,
// already used, or no images either.
func (c *Cursor) useFallback(hdrNits float64) (bool, error) {
	if c.alloc != allocDumb || c.fallback == nil {
		return false, nil
	}
	imgs, err := c.fallback()
	if err != nil {
		return false, err
	}
	c.imgs.close()
	c.install(imgs, allocVulkan)
	last := c.last
	c.last = laterImage{}
	l := c.later
	if l == nil && c.image {
		l = &last
	}
	c.image, c.later = false, nil
	if l == nil {
		return true, nil
	}
	return true, c.setImage(l.pixels, l.w, l.h, l.hotX, l.hotY, hdrNits)
}

// setImage loads premultiplied ARGB8888 pixels into an image neither on
// screen nor in flight; when there is none it waits for the next commit
// event (flushLater). An empty image hides the cursor (a client asked for
// none).
// hdrNits is the SDR white of an HDR output (0: SDR).
func (c *Cursor) setImage(pixels []byte, w, h, hotX, hotY int, hdrNits float64) error {
	c.later = nil
	if w <= 0 || h <= 0 || len(pixels) < w*h*4 {
		c.image = false
		c.last = laterImage{}
		return nil
	}
	if c.imgs == nil {
		return errors.New("no cursor images")
	}
	next := c.freeSlot()
	if next < 0 {
		c.later = &laterImage{pixels: slices.Clone(pixels[:w*h*4]), w: w, h: h, hotX: hotX, hotY: hotY}
		return nil
	}
	if err := c.imgs.write(next, pixels, w, h, hdrNits); err != nil {
		return err
	}
	c.cur, c.image, c.hotX, c.hotY = next, true, hotX, hotY
	if c.alloc == allocDumb && !c.off {
		c.last = laterImage{pixels: append(c.last.pixels[:0], pixels[:w*h*4]...), w: w, h: h, hotX: hotX, hotY: hotY}
	}
	return nil
}

// freeSlot is an image the plane neither shows nor is about to show, -1
// when both are busy.
func (c *Cursor) freeSlot() int {
	for i, fb := range c.fbs {
		if fb != c.screen && !(c.flying && c.flight.on && c.flight.fb == fb) {
			return i
		}
	}
	return -1
}

// flushLater loads an image that waited for a free slot. It reports
// whether the cursor changed.
func (c *Cursor) flushLater(hdrNits float64) (bool, error) {
	l := c.later
	if l == nil || c.freeSlot() < 0 {
		return false, nil
	}
	return true, c.setImage(l.pixels, l.w, l.h, l.hotX, l.hotY, hdrNits)
}

// committed records that a commit carrying state s is in flight.
func (c *Cursor) committed(s cursorState) {
	if s != c.applied {
		c.mu.Lock()
		c.commits++
		c.mu.Unlock()
	}
	c.applied, c.flight, c.flying = s, s, true
}

// landed records that the pending commit reached the screen.
func (c *Cursor) landed() {
	if c.flying {
		c.screen, c.flying = 0, false
		if c.flight.on {
			c.screen = c.flight.fb
		}
	}
}

// desired is what the cursor plane should show now.
func (c *Cursor) desired() cursorState {
	c.mu.Lock()
	x, y, away := c.x, c.y, c.away
	c.mu.Unlock()
	if !c.image || away || c.off || c.fbs[c.cur] == 0 {
		return cursorState{}
	}
	return cursorState{on: true, fb: c.fbs[c.cur], x: x - c.hotX, y: y - c.hotY}
}

// props adds the cursor plane in state s to req.
func (c *Cursor) props(req *atomicReq, crtc uint32, s cursorState) {
	p := c.plane
	if !s.on {
		req.set(p.id, p.prop("FB_ID"), 0)
		req.set(p.id, p.prop("CRTC_ID"), 0)
		return
	}
	size := uint64(c.size)
	req.set(p.id, p.prop("FB_ID"), uint64(s.fb))
	req.set(p.id, p.prop("CRTC_ID"), uint64(crtc))
	req.set(p.id, p.prop("SRC_X"), 0)
	req.set(p.id, p.prop("SRC_Y"), 0)
	req.set(p.id, p.prop("SRC_W"), size<<16)
	req.set(p.id, p.prop("SRC_H"), size<<16)
	req.set(p.id, p.prop("CRTC_X"), uint64(int64(s.x)))
	req.set(p.id, p.prop("CRTC_Y"), uint64(int64(s.y)))
	req.set(p.id, p.prop("CRTC_W"), size)
	req.set(p.id, p.prop("CRTC_H"), size)
}

// free removes the framebuffers and releases the images.
func (c *Cursor) free() {
	if c.imgs != nil {
		c.imgs.close()
		c.imgs, c.alloc = nil, ""
	}
	c.fbs = [2]uint32{}
	c.image, c.later = false, nil
	c.last = laterImage{}
	c.screen, c.flying = 0, false
}
