package drm

import (
	"errors"
	"fmt"

	"github.com/bnema/neferwl/internal/adapters/hdrcursor"
	"github.com/bnema/neferwl/internal/ports"
)

// Who allocated the cursor images (the "alloc" log field).
//
// The renderer's cursor images (allocVulkan, shared with the output images)
// are the fallback.
const allocDumb = "dumb"

// cursorImages are the two ARGB8888 images of the cursor plane and their
// framebuffers. The output goroutine owns them.
type cursorImages interface {
	// fb is the framebuffer of image i.
	fb(i int) uint32
	// write fills image i with premultiplied ARGB8888 pixels (w×h, w*4 per
	// row), cropped to the image and cleared around. hdrNits > 0 converts
	// them to the HDR10 output encoding for that SDR white.
	write(i int, pixels []byte, w, h int, hdrNits float64) error
	// close removes the framebuffers and releases the images.
	close()
}

// dumbImage is one KMS dumb buffer mapped for the CPU.
type dumbImage struct {
	handle, fb uint32
	pitch      int
	mem        []byte
}

// dumbCursor are two KMS dumb buffers: KMS allocates what it scans out, the
// CPU writes the pixels. Renderer-allocated images are refused by some
// drivers (nvidia-drm) on the cursor plane.
type dumbCursor struct {
	k     kms
	size  int
	img   [2]dumbImage
	cache hdrcursor.Cache
}

func newDumbCursor(k kms, size int) (*dumbCursor, error) {
	d := &dumbCursor{k: k, size: size}
	for i := range d.img {
		if err := d.alloc(&d.img[i]); err != nil {
			d.close()
			return nil, err
		}
	}
	return d, nil
}

func (d *dumbCursor) alloc(img *dumbImage) error {
	b, err := d.k.createDumb(d.size, d.size)
	if err != nil {
		return fmt.Errorf("create dumb buffer: %w", err)
	}
	img.handle, img.pitch = b.handle, int(b.pitch)
	if img.pitch < d.size*4 {
		return fmt.Errorf("dumb buffer pitch %d, want at least %d", img.pitch, d.size*4)
	}
	if img.mem, err = d.k.mapDumb(b); err != nil {
		return fmt.Errorf("map dumb buffer: %w", err)
	}
	if len(img.mem) < img.pitch*d.size {
		return fmt.Errorf("dumb buffer mapping %d bytes, want %d", len(img.mem), img.pitch*d.size)
	}
	clear(img.mem)
	if img.fb, err = d.k.addFBHandle(b, d.size, d.size, fourccARGB); err != nil {
		return fmt.Errorf("dumb buffer framebuffer: %w", err)
	}
	return nil
}

func (d *dumbCursor) fb(i int) uint32 { return d.img[i].fb }

func (d *dumbCursor) write(i int, pixels []byte, w, h int, hdrNits float64) error {
	if i < 0 || i >= len(d.img) || d.img[i].mem == nil {
		return errors.New("no cursor image")
	}
	if w < 0 || h < 0 || len(pixels) < w*h*4 {
		return errors.New("short cursor image")
	}
	img := &d.img[i]
	mem := img.mem[:img.pitch*d.size]
	clear(mem)
	cw := min(w, d.size) * 4
	src, stride := pixels, w*4
	if hdrNits > 0 {
		// Called only on image changes, never on moves.
		src, stride = d.cache.Convert(pixels, w, h, d.size, hdrNits), cw
	}
	for y := range min(h, d.size) {
		copy(mem[y*img.pitch:y*img.pitch+cw], src[y*stride:y*stride+cw])
	}
	return nil
}

func (d *dumbCursor) close() {
	for i := range d.img {
		img := &d.img[i]
		if img.fb != 0 {
			_ = d.k.rmFB(img.fb)
		}
		if img.mem != nil {
			_ = d.k.unmapDumb(img.mem)
		}
		if img.handle != 0 {
			_ = d.k.destroyDumb(img.handle)
		}
		*img = dumbImage{}
	}
	d.cache.Reset()
}

// rendererCursor are the renderer's cursor images (linear dmabufs the
// renderer allocates and writes), imported as framebuffers.
type rendererCursor struct {
	k   kms
	r   ports.Renderer
	fbs [2]uint32
}

func newRendererCursor(k kms, r ports.Renderer, size int) (*rendererCursor, error) {
	bufs, err := r.CursorBuffers(size)
	if err != nil {
		return nil, err
	}
	c := &rendererCursor{k: k, r: r}
	for i := range bufs {
		if err == nil {
			c.fbs[i], err = k.addFB(&bufs[i], fourccARGB)
		}
		for _, p := range bufs[i].Planes {
			p.File.Close()
		}
	}
	if err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *rendererCursor) fb(i int) uint32 { return c.fbs[i] }

// write relies on the renderer's own HDR state: hdrNits is not used.
func (c *rendererCursor) write(i int, pixels []byte, w, h int, _ float64) error {
	return c.r.WriteCursor(i, pixels, w, h)
}

func (c *rendererCursor) close() {
	for i, fb := range c.fbs {
		if fb != 0 {
			_ = c.k.rmFB(fb)
			c.fbs[i] = 0
		}
	}
}
