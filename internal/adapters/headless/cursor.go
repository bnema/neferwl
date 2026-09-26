package headless

import (
	"image"
	"sync"

	"github.com/bnema/neferwl/internal/ports"
)

// Cursor is the software cursor drawn into screenshots; it plays the role of
// the DRM cursor plane. Move is called from the input goroutine.
type Cursor struct {
	mu     sync.Mutex
	x, y   float64
	img    ports.CursorImage
	hidden bool
}

func (c *Cursor) Move(x, y float64) {
	c.mu.Lock()
	c.x, c.y, c.hidden = x, y, false
	c.mu.Unlock()
}

// Hide stops drawing the cursor until the next move.
func (c *Cursor) Hide() {
	c.mu.Lock()
	c.hidden = true
	c.mu.Unlock()
}

func (c *Cursor) set(img ports.CursorImage) {
	c.mu.Lock()
	c.img = img
	c.mu.Unlock()
}

// draw blends the premultiplied cursor over dst with its hotspot at (x, y).
func (c *Cursor) draw(dst *image.RGBA) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hidden {
		return
	}
	img := c.img
	ox, oy := int(c.x)-img.HotX, int(c.y)-img.HotY
	b := dst.Bounds()
	for y := range img.H {
		for x := range img.W {
			dx, dy := ox+x, oy+y
			if !image.Pt(dx, dy).In(b) {
				continue
			}
			s := img.Pixels[(y*img.W+x)*4:]
			a := int(s[3])
			if a == 0 {
				continue
			}
			d := dst.Pix[dst.PixOffset(dx, dy):]
			// Source is premultiplied B,G,R,A; dst is straight R,G,B,A (opaque).
			// min guards against themes that are not really premultiplied.
			d[0] = byte(min(255, int(s[2])+int(d[0])*(255-a)/255))
			d[1] = byte(min(255, int(s[1])+int(d[1])*(255-a)/255))
			d[2] = byte(min(255, int(s[0])+int(d[2])*(255-a)/255))
		}
	}
}
