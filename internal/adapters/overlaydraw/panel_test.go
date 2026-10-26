package overlaydraw

import (
	"bytes"
	"image/color"
	"math"
	"testing"
)

func TestPanelLayout(t *testing.T) {
	style := Style{FontPx: 13, Background: color.RGBA{20, 40, 60, 255}, Foreground: color.RGBA{240, 230, 220, 255}}
	var p Panel
	lines := [4]string{"123"}
	pix, w, h := p.Draw(lines, [64]uint16{}, 1, style)
	if len(pix) != w*h*4 || pix[3] != 0 {
		t.Fatal("invalid buffer or opaque corner")
	}
	saved := bytes.Clone(pix)
	again, ww, hh := p.Draw(lines, [64]uint16{}, 1, style)
	if ww != w || hh != h || !bytes.Equal(saved, again) || &pix[0] != &again[0] {
		t.Fatal("redraw changed pixels, dimensions, or storage")
	}
	_, wide, tall := p.Draw([4]string{"123456789"}, [64]uint16{}, 1, style)
	if wide <= w || tall != h {
		t.Fatal("longest line did not grow width only")
	}
	_, _, skipped := p.Draw([4]string{"", "123", "", ""}, [64]uint16{}, 1, style)
	if skipped != h {
		t.Fatal("empty lines not skipped")
	}
	var graph [64]uint16
	graph[0], graph[1] = 100, 50
	pix, ww, hh = p.Draw(lines, graph, 1, style)
	lh := p.faces[13].Metrics().Height.Ceil()
	pad := int(math.Round(.6 * float64(lh)))
	if hh != h+2*lh || ww < 128+2*pad {
		t.Fatal("graph dimensions incorrect")
	}
	bg := panelAlpha(style.Background, .85)
	i := ((pad+lh)*ww + pad) * 4
	if bytes.Equal(pix[i:i+4], []byte{bg.B, bg.G, bg.R, bg.A}) {
		t.Fatal("bar pixel is background")
	}
	// The second bar is half-height, so its top remains background.
	j := ((pad+lh)*ww + pad + 2) * 4
	if !bytes.Equal(pix[j:j+4], []byte{bg.B, bg.G, bg.R, bg.A}) {
		t.Fatal("bar heights are not proportional")
	}
	_, _, zeroHeight := p.Draw(lines, [64]uint16{}, 1, style)
	if zeroHeight != h {
		t.Fatal("zero graph was not omitted")
	}
}

func TestPanelRedrawAllocations(t *testing.T) {
	bg, fg := Colors("", "", "")
	style := Style{13, bg, fg}
	var p Panel
	texts := [2][4]string{{"0123456789"}, {"9876543210"}}
	var graph [64]uint16
	graph[0] = 1
	for _, lines := range texts {
		p.Draw(lines, graph, 1, style)
	}
	i := 0
	if allocs := testing.AllocsPerRun(50, func() { p.Draw(texts[i%2], graph, 1, style); i++ }); allocs != 0 {
		t.Fatalf("redraw allocations = %v, want 0", allocs)
	}
}
