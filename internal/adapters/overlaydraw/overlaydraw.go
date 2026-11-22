// Package overlaydraw rasterizes compositor overlays (such as keyboard hint
// labels) on the CPU as premultiplied BGRA pixels for the renderer to upload
// and draw.
package overlaydraw

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
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
	gen   uint64
	cache map[key]entry
	// raw maps a key with the caller's MaxWidth to its cache key, so a
	// hit measures nothing. It is cleared with cache.
	raw map[key]key
}

// fitCalls counts fitWidth calls; tests check that cache hits measure
// nothing.
var fitCalls int

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
	style := Style{FontPx: fontSize * scale, Background: bg, Foreground: fg}
	if len(styles) > 0 {
		style = styles[0]
	}
	if st := currentFont(); st.gen != r.gen {
		closeFaces(&r.faces)
		r.gen, r.cache, r.raw = st.gen, nil, nil
	}
	rk := key{text, typed, scale, style}
	if k, ok := r.raw[rk]; ok {
		if e, ok := r.cache[k]; ok {
			return e.pix, e.w, e.h
		}
	}
	face := r.face(style.FontPx)
	if face == nil {
		return nil, 0, 0
	}
	style.MaxWidth = fitWidth(face, text, style.MaxWidth)
	k := key{text, typed, scale, style}
	if r.cache == nil || len(r.cache) >= maxEntries || len(r.raw) >= 4*maxEntries {
		r.cache, r.raw = map[key]entry{}, map[key]key{}
	}
	r.raw[rk] = k
	if e, ok := r.cache[k]; ok {
		return e.pix, e.w, e.h
	}
	e := render(face, text, typed, style)
	r.cache[k] = e
	return e.pix, e.w, e.h
}

func (r *Raster) face(scale float64) font.Face {
	return cachedFace(&r.faces, scale)
}

// cachedFace shares font loading without sharing mutable faces between
// owners. The owner drops its faces when the font changes (FontGen).
func cachedFace(faces *map[float64]font.Face, scale float64) font.Face {
	if f, ok := (*faces)[scale]; ok {
		return f
	}
	parsed := currentFont().font
	if parsed == nil {
		return nil
	}
	f, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: scale, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	if *faces == nil || len(*faces) >= 16 {
		closeFaces(faces)
	}
	(*faces)[scale] = f
	return f
}

// closeFaces closes and empties faces.
func closeFaces(faces *map[float64]font.Face) {
	for _, old := range *faces {
		_ = old.Close()
	}
	*faces = map[float64]font.Face{}
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
	if style.PadX > 0 {
		px = int(math.Round(style.PadX))
	}
	if style.PadY > 0 {
		py = int(math.Round(style.PadY))
	}
	border := 1
	if style.TextOnly {
		border = 0
	}
	if style.MaxWidth > 0 {
		text = truncate(face, text, style.MaxWidth)
		typed = min(typed, len(text))
	}
	adv := font.MeasureString(face, text).Ceil()
	w := adv + 2*px + 2*border
	h := m.Height.Ceil() + 2*py + 2*border
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if !style.TextOnly {
		draw.Draw(img, img.Bounds(), image.NewUniform(outline), image.Point{}, draw.Src)
		draw.Draw(img, img.Bounds().Inset(border), image.NewUniform(background), image.Point{}, draw.Src)
	}
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

// fitWidth normalises a MaxWidth so every width that cuts text the same
// way is one value (and one cache key): 0 when text fits, else maxWidth
// floored to a multiple of the font's glyph advance (the font is
// monospace).
func fitWidth(face font.Face, text string, maxWidth int) int {
	fitCalls++
	if maxWidth <= 0 || font.MeasureString(face, text).Ceil() <= maxWidth {
		return 0
	}
	adv, ok := face.GlyphAdvance('M')
	if !ok || adv <= 0 {
		return maxWidth
	}
	return max((fixed.I(maxWidth) / adv * adv).Floor(), 1)
}

// ellipsis ends a truncated text.
const ellipsis = "\u2026"

// truncate cuts text at a rune boundary so that it, with the ellipsis,
// draws at most maxWidth physical pixels wide; text that fits is returned
// as is. Only a cache miss calls it.
func truncate(face font.Face, text string, maxWidth int) string {
	if font.MeasureString(face, text).Ceil() <= maxWidth {
		return text
	}
	limit := fixed.I(maxWidth) - font.MeasureString(face, ellipsis)
	var width fixed.Int26_6
	prev := rune(-1)
	for i, c := range text {
		if prev >= 0 {
			width += face.Kern(prev, c)
		}
		a, ok := face.GlyphAdvance(c)
		if !ok {
			a, _ = face.GlyphAdvance('?')
		}
		if width+a > limit {
			if i == 0 {
				return ""
			}
			return text[:i] + ellipsis
		}
		width += a
		prev = c
	}
	return text
}

// Len is the number of cached labels.
func (r *Raster) Len() int { return len(r.cache) }

// maxTitles bounds the Titles cache, one entry per window, as a backstop
// to Sweep.
const maxTitles = 256

// Titles draws and caches window titles: one raster per window, replaced
// when its title or style changes, so titles that churn do not fill a
// cache. It is not safe for concurrent use: the renderer owns it.
type Titles struct {
	faces map[float64]font.Face
	gen   uint64
	m     map[uint64]titleEntry
}

type titleEntry struct {
	text string
	// raw is the caller's style, style the one drawn (MaxWidth
	// normalised); a call with the same text and raw style is a hit.
	raw, style Style
	e          entry
	// used is the frame of the last call.
	used uint64
}

// Title returns window id's title raster, like Raster.Label without typed
// characters; style.FontPx and MaxWidth are physical. frame is the
// renderer's frame counter, read by Sweep. The slice stays valid until
// the next call for id or a Sweep.
func (t *Titles) Title(id uint64, text string, style Style, frame uint64) (pix []byte, w, h int) {
	if text == "" {
		return nil, 0, 0
	}
	if st := currentFont(); st.gen != t.gen {
		closeFaces(&t.faces)
		t.gen, t.m = st.gen, nil
	}
	old, ok := t.m[id]
	if ok && old.text == text && old.raw == style {
		old.used = frame
		t.m[id] = old
		return old.e.pix, old.e.w, old.e.h
	}
	face := cachedFace(&t.faces, style.FontPx)
	if face == nil {
		return nil, 0, 0
	}
	raw := style
	style.MaxWidth = fitWidth(face, text, style.MaxWidth)
	if ok && old.text == text && old.style == style {
		old.raw, old.used = raw, frame
		t.m[id] = old
		return old.e.pix, old.e.w, old.e.h
	}
	if t.m == nil || !ok && len(t.m) >= maxTitles {
		t.m = map[uint64]titleEntry{}
	}
	e := render(face, text, 0, style)
	t.m[id] = titleEntry{text: text, raw: raw, style: style, e: e, used: frame}
	return e.pix, e.w, e.h
}

// Sweep drops the titles not drawn since frame-ttl.
func (t *Titles) Sweep(frame, ttl uint64) {
	for id, e := range t.m {
		if frame-e.used > ttl {
			delete(t.m, id)
		}
	}
}

// Len is the number of cached titles.
func (t *Titles) Len() int { return len(t.m) }

// Bytes is the size of the cached title rasters.
func (t *Titles) Bytes() int {
	n := 0
	for _, e := range t.m {
		n += len(e.e.pix)
	}
	return n
}
