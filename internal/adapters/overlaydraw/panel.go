package overlaydraw

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// Panel rasterizes a window info panel: a rounded rectangle holding up to
// len(lines) text lines and, when graph has a non-zero sample, a frame-time
// sparkline under them. It draws into its own buffer, reused while the size
// stays the same: a panel redrawn with new text allocates nothing.
// It is not safe for concurrent use; the caller owns it.
type Panel struct {
	faces      map[float64]font.Face
	img        image.RGBA
	foreground color.RGBA
	ink        image.Uniform
	drawer     font.Drawer
}

// Draw returns premultiplied BGRA pixels owned by p and valid until the next
// Draw call. Empty lines are skipped; an all-zero graph is omitted. Invalid
// scales use 1. A font that cannot load yields a nil slice.
func (p *Panel) Draw(lines [4]string, graph [64]uint16, scale float64, style Style) (pix []byte, w, h int) {
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	face := cachedFace(&p.faces, style.FontPx)
	if face == nil {
		return nil, 0, 0
	}
	metrics := face.Metrics()
	lineHeight := metrics.Height.Ceil()
	padding := int(math.Round(.6 * float64(lineHeight)))
	radius := float64(lineHeight) * .5
	count, widest := 0, 0
	for _, line := range lines {
		if line != "" {
			count++
			widest = max(widest, font.MeasureString(face, line).Ceil())
		}
	}
	var peak uint16
	for _, sample := range graph {
		peak = max(peak, sample)
	}
	step := max(2, int(math.Round(2*scale)))
	graphWidth, graphHeight := 0, 0
	if peak != 0 {
		graphWidth, graphHeight = len(graph)*step, 2*lineHeight
	}
	w = max(widest, graphWidth) + 2*padding
	h = count*lineHeight + graphHeight + 2*padding
	size := w * h * 4
	if p.img.Rect.Dx() != w || p.img.Rect.Dy() != h || cap(p.img.Pix) < size {
		p.img.Pix = make([]byte, size)
	} else {
		p.img.Pix = p.img.Pix[:size]
		clear(p.img.Pix)
	}
	p.img.Stride, p.img.Rect = w*4, image.Rect(0, 0, w, h)
	bg := panelAlpha(style.Background, .85)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := math.Max(radius-(float64(x)+.5), math.Max(float64(x)+.5-(float64(w)-radius), 0))
			dy := math.Max(radius-(float64(y)+.5), math.Max(float64(y)+.5-(float64(h)-radius), 0))
			if dx*dx+dy*dy <= radius*radius {
				p.img.SetRGBA(x, y, bg)
			}
		}
	}
	p.foreground = style.Foreground
	p.ink.C = &p.foreground
	p.drawer.Dst, p.drawer.Src, p.drawer.Face = &p.img, &p.ink, face
	row := 0
	for _, line := range lines {
		if line != "" {
			p.drawer.Dot = fixed.Point26_6{X: fixed.I(padding), Y: fixed.I(padding+row*lineHeight) + metrics.Ascent}
			p.drawer.DrawString(line)
			row++
		}
	}
	if peak != 0 {
		bottom := padding + count*lineHeight + graphHeight - 1
		baseline := panelAlpha(style.Foreground, .15)
		bar := panelAlpha(style.Foreground, .7)
		for x := padding; x < padding+graphWidth; x++ {
			p.over(x, bottom, baseline)
		}
		for i, sample := range graph {
			height := int(math.Round(float64(sample) * float64(graphHeight) / float64(peak)))
			for y := bottom - height + 1; y <= bottom; y++ {
				for x := padding + i*step; x < padding+(i+1)*step-1; x++ {
					p.over(x, y, bar)
				}
			}
		}
	}
	for i := 0; i < size; i += 4 {
		p.img.Pix[i], p.img.Pix[i+2] = p.img.Pix[i+2], p.img.Pix[i]
	}
	return p.img.Pix, w, h
}

// panelAlpha scales premultiplied channels together with alpha.
func panelAlpha(c color.RGBA, opacity float64) color.RGBA {
	return color.RGBA{uint8(math.Round(float64(c.R) * opacity)), uint8(math.Round(float64(c.G) * opacity)), uint8(math.Round(float64(c.B) * opacity)), uint8(math.Round(float64(c.A) * opacity))}
}

// over composites premultiplied ink without allocating a draw source.
func (p *Panel) over(x, y int, c color.RGBA) {
	i := y*p.img.Stride + x*4
	inv := uint32(255 - c.A)
	for j, v := range [4]uint8{c.R, c.G, c.B, c.A} {
		p.img.Pix[i+j] = uint8(uint32(v) + (uint32(p.img.Pix[i+j])*inv+127)/255)
	}
}
