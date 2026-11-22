package overlaydraw

import "testing"

// TextOnly draws glyphs on a transparent raster with the given padding;
// the default keeps the opaque box.
func TestLabelTextOnly(t *testing.T) {
	var r Raster
	bg, fg := Colors("#3366ff", "", "")
	box, bw, bh := r.Label("swap", 0, 1, Style{FontPx: 13, Background: bg, Foreground: fg})
	if box[3] == 0 || box[(bh/2*bw+2)*4+3] != 255 {
		t.Fatal("hint label lost its box")
	}
	pix, w, h := r.Label("swap", 0, 1, Style{FontPx: 13, Background: bg, Foreground: fg, PadX: 8, PadY: 4, TextOnly: true})
	if pix[3] != 0 || pix[((h-1)*w+w-1)*4+3] != 0 {
		t.Fatal("text-only raster has a background")
	}
	ink := 0
	for i := 3; i < len(pix); i += 4 {
		if pix[i] > 0 {
			ink++
		}
	}
	if ink == 0 {
		t.Fatal("no glyphs")
	}
	if w != bw-2-2*3+2*8 {
		t.Fatalf("width %d, box %d: padding not 8", w, bw)
	}
}
