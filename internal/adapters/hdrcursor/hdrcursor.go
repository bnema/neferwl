// Package hdrcursor converts cursor images to the HDR10 output encoding
// (BT.2020 PQ) for cursor planes, whichever adapter owns the image memory.
package hdrcursor

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
)

var srgbTable = func() (table [256]float64) {
	for i := range table {
		table[i] = SRGBToLinear(float64(i) / 255)
	}
	return
}()

// SRGBToLinear decodes a unit-range sRGB component.
func SRGBToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// PQEncode encodes absolute luminance in nits using SMPTE ST 2084.
func PQEncode(nits float64) float64 {
	if nits <= 0 {
		return 0
	}
	x := math.Pow(min(nits, 10000)/10000, 2610.0/16384)
	return math.Pow((3424.0/4096+2413.0/128*x)/(1+2392.0/128*x), 2523.0/32)
}

// Pixel converts premultiplied BGRA bytes in place, preserving alpha. The
// source is quantized to its 8-bit unassociated sRGB value for the LUT.
func Pixel(p []byte, nits float64) {
	a := float64(p[3]) / 255
	if a == 0 {
		clear(p[:3])
		return
	}
	index := func(v byte) int { return min(255, int(math.Round(float64(v)/a))) }
	r, g, b := srgbTable[index(p[2])], srgbTable[index(p[1])], srgbTable[index(p[0])]
	r, g, b = PQEncode((0.627404*r+0.329283*g+0.043313*b)*nits), PQEncode((0.069097*r+0.919540*g+0.011362*b)*nits), PQEncode((0.016391*r+0.088013*g+0.895595*b)*nits)
	p[0], p[1], p[2] = byte(math.Round(b*a*255)), byte(math.Round(g*a*255)), byte(math.Round(r*a*255))
}

// Cache converts cursor images and keeps only the last result, bounded by
// the cursor plane's maximum side regardless of cursor theme size. The
// zero value is ready; it is not safe for concurrent use.
type Cache struct {
	key    [32]byte
	pixels []byte
}

// Len is the size of the cached conversion in bytes (0: empty).
func (c *Cache) Len() int { return len(c.pixels) }

// Key identifies the cached conversion (the zero key: empty).
func (c *Cache) Key() [32]byte { return c.key }

// Reset drops the cached conversion.
func (c *Cache) Reset() { *c = Cache{} }

func key(pixels []byte, w, h, side int, nits float64) [32]byte {
	var header [32]byte
	binary.LittleEndian.PutUint64(header[:8], uint64(w))
	binary.LittleEndian.PutUint64(header[8:16], uint64(h))
	binary.LittleEndian.PutUint64(header[16:24], math.Float64bits(nits))
	binary.LittleEndian.PutUint64(header[24:], uint64(side))
	digest := sha256.New()
	_, _ = digest.Write(header[:])
	_, _ = digest.Write(pixels)
	var k [32]byte
	digest.Sum(k[:0])
	return k
}

// Convert returns the w×h premultiplied BGRA image (w*4 per row) cropped
// to side×side and converted for an SDR white of nits, packed without row
// padding: min(w, side) pixels per row, min(h, side) rows. The result is
// owned by the cache and valid until the next call.
func (c *Cache) Convert(pixels []byte, w, h, side int, nits float64) []byte {
	pixels = pixels[:w*h*4]
	k := key(pixels, w, h, side, nits)
	if c.pixels != nil && c.key == k {
		return c.pixels
	}
	cw, ch := min(w, side)*4, min(h, side)
	out := make([]byte, cw*ch)
	for y := range ch {
		copy(out[y*cw:(y+1)*cw], pixels[y*w*4:y*w*4+cw])
		for x := 0; x < cw; x += 4 {
			Pixel(out[y*cw+x:y*cw+x+4], nits)
		}
	}
	c.key, c.pixels = k, out
	return out
}
