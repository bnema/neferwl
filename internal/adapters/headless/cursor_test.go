package headless

import (
	"image"
	"image/color"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

func TestCursorDraw(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for i := range dst.Pix {
		dst.Pix[i] = 255
	}
	// 2x2: opaque red, transparent, half black, opaque blue; hotspot (1,1).
	c := &Cursor{}
	c.set(ports.CursorImage{W: 2, H: 2, HotX: 1, HotY: 1, Pixels: []byte{0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 128, 255, 0, 0, 255}})
	c.Move(5, 5)
	c.draw(dst)
	for p, want := range map[image.Point]color.RGBA{{4, 4}: {255, 0, 0, 255}, {5, 4}: {255, 255, 255, 255}, {4, 5}: {127, 127, 127, 255}, {5, 5}: {0, 0, 255, 255}} {
		if got := dst.RGBAAt(p.X, p.Y); got != want {
			t.Errorf("%v = %v, want %v", p, got, want)
		}
	}
	// Off the edge: clipped, no panic.
	c.Move(0, 0)
	c.draw(dst)
	c.Move(100, -3)
	c.draw(dst)
}
