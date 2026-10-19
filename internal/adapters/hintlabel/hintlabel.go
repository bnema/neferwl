// Package hintlabel rasterizes the keyboard hint labels on the CPU: a small
// box with the label text in Go Mono, as premultiplied BGRA pixels for the
// renderer to upload and draw.
package hintlabel

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	// fontSize is the text size in logical pixels.
	fontSize = 13
	// padX and padY are the padding around the text in logical pixels.
	padX, padY = 3, 1
	// maxEntries bounds the cache: it is cleared when it grows past it.
	maxEntries = 1024
)

var (
	parseOnce sync.Once
	parsed    *opentype.Font
	parseErr  error
)

type key struct {
	text  string
	typed int
	scale float64
	style Style
}

type entry struct {
	pix  []byte
	w, h int
}

// Raster draws and caches labels. It is not safe for concurrent use: the
// renderer owns it.
type Raster struct {
	faces map[float64]font.Face
	cache map[key]entry
}

// Label returns the label box for text, whose first typed characters are
// drawn dimmed, as premultiplied BGRA at scale physical pixels per logical
// pixel. The slice is owned by the cache and stays valid until the cache is
// cleared by a later call: do not modify it. Empty text, or a font that does
// not load, yields a nil slice.
func (r *Raster) Label(text string, typed int, scale float64, styles ...Style) (pix []byte, w, h int) {
	if text == "" {
		return nil, 0, 0
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	typed = max(0, min(typed, len(text)))
	bg, fg := Colors("", "", "")
	style := Style{fontSize * scale, bg, fg}
	if len(styles) > 0 {
		style = styles[0]
	}
	k := key{text, typed, scale, style}
	if e, ok := r.cache[k]; ok {
		return e.pix, e.w, e.h
	}
	face := r.face(style.FontPx)
	if face == nil {
		return nil, 0, 0
	}
	if r.cache == nil || len(r.cache) >= maxEntries {
		r.cache = map[key]entry{}
	}
	e := render(face, text, typed, style)
	r.cache[k] = e
	return e.pix, e.w, e.h
}

func (r *Raster) face(scale float64) font.Face {
	if f, ok := r.faces[scale]; ok {
		return f
	}
	parseOnce.Do(func() { parsed, parseErr = opentype.Parse(gomono.TTF) })
	if parseErr != nil {
		return nil
	}
	f, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: scale, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	if r.faces == nil || len(r.faces) >= 16 {
		for _, old := range r.faces {
			_ = old.Close()
		}
		r.faces = map[float64]font.Face{}
	}
	r.faces[scale] = f
	return f
}

// render draws an opaque box inside a one-physical-pixel translucent outline.
// All colours are premultiplied.
func render(face font.Face, text string, typed int, style Style) entry {
	scale := style.FontPx / fontSize
	background, ink := style.Background, style.Foreground
	typedInk := mix(ink, background)
	outline := color.RGBA{uint8(math.Round(float64(ink.R) * .35)), uint8(math.Round(float64(ink.G) * .35)), uint8(math.Round(float64(ink.B) * .35)), 89}
	m := face.Metrics()
	px := int(math.Round(padX * scale))
	py := int(math.Round(padY * scale))
	border := 1
	adv := font.MeasureString(face, text).Ceil()
	w := adv + 2*px + 2*border
	h := m.Height.Ceil() + 2*py + 2*border
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(outline), image.Point{}, draw.Src)
	draw.Draw(img, img.Bounds().Inset(border), image.NewUniform(background), image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Face: face, Dot: fixed.Point26_6{X: fixed.I(border + px), Y: fixed.I(border+py) + m.Ascent}}
	d.Src = image.NewUniform(typedInk)
	d.DrawString(text[:typed])
	d.Src = image.NewUniform(ink)
	d.DrawString(text[typed:])
	// Premultiplied RGBA to BGRA in place.
	pix := img.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i], pix[i+2] = pix[i+2], pix[i]
	}
	return entry{pix: pix, w: w, h: h}
}
