package hdrcursor

import (
	"bytes"
	"math"
	"testing"
)

// vector is a 5×3 premultiplied BGRA image: alpha ramps up with one fully
// transparent pixel (index 3).
func vector() (pixels []byte, w, h int) {
	w, h = 5, 3
	pixels = make([]byte, w*h*4)
	for i := range w * h {
		a := byte(i * 255 / (w*h - 1))
		if i == 3 {
			a = 0
		}
		c := func(v int) byte { return byte(v * int(a) / 255) }
		pixels[i*4], pixels[i*4+1], pixels[i*4+2], pixels[i*4+3] = c(i*37%256), c((i*91+20)%256), c((i*53+200)%256), a
	}
	return
}

// The conversion is byte-identical to the one the Vulkan renderer did
// before it moved here (203 nits, cropped to 4×4).
func TestConvertMatchesRecordedVector(t *testing.T) {
	pixels, w, h := vector()
	want := []byte{0x0, 0x0, 0x0, 0x0, 0x5, 0x7, 0x9, 0x12, 0xd, 0x13, 0xf, 0x24, 0x0, 0x0, 0x0, 0x0, 0x2e, 0x31, 0x31, 0x5b, 0x3a, 0x1f, 0x20, 0x6d, 0x20, 0x39, 0x30, 0x7f, 0x33, 0x51, 0x46, 0x91, 0x4c, 0x58, 0x5f, 0xb6, 0x5f, 0x72, 0x5e, 0xc8, 0x6d, 0x4e, 0x4c, 0xda, 0x81, 0x75, 0x6c, 0xec}
	var c Cache
	if got := c.Convert(pixels, w, h, 4, 203); !bytes.Equal(got, want) {
		t.Fatalf("converted\n got %#v\nwant %#v", got, want)
	}
}

func TestPixelWhite(t *testing.T) {
	p := []byte{255, 255, 255, 255}
	Pixel(p, 203)
	if p[3] != 255 || math.Abs(float64(p[0])-148) > 1 || p[0] != p[1] || p[1] != p[2] {
		t.Fatalf("cursor white: %v", p)
	}
	if PQEncode(0) != 0 || math.Abs(PQEncode(10000)-1) > 1e-9 {
		t.Fatal("PQ endpoints")
	}
}

func TestCacheKeepsLastConversion(t *testing.T) {
	var c Cache
	px := []byte{255, 255, 255, 255}
	a := c.Convert(px, 1, 1, 64, 203)
	if c.Len() != 4 || a[0] != 148 {
		t.Fatalf("len %d pixel %v", c.Len(), a)
	}
	first := c.Key()
	if b := c.Convert(px, 1, 1, 64, 203); &a[0] != &b[0] {
		t.Fatal("same image converted again")
	}
	for i := range 12 {
		v := byte(i * 18)
		c.Convert([]byte{v, v, v, 255}, 1, 1, 64, 203)
		if c.Len() != 4 {
			t.Fatalf("cache grew after image %d: %d", i, c.Len())
		}
	}
	if c.Key() == first {
		t.Fatal("conversion not replaced")
	}
	c.Convert(px, 1, 1, 64, 100)
	if c.Key() == first {
		t.Fatal("nits not part of the key")
	}
	c.Reset()
	if c.Len() != 0 || c.Key() != [32]byte{} {
		t.Fatal("reset left a conversion")
	}
}
