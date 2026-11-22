package vulkan

import (
	"image"
	"image/color"
	"math"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"github.com/bnema/neferwl/internal/domain"
)

// tabTitleOverlay tags the GPU copy of a tab title: with the window ID in
// the low bits, it stays clear of core overlay IDs (below 1<<62 + n) and of
// dropLabelOverlay.
const tabTitleOverlay uint64 = 1<<63 | 1<<62

// tabTitlePadX is the space left of a tab title and the least right of it,
// logical px.
const tabTitlePadX = 6

// tabTitlePadY is the space above and below a title, logical px: core's
// tabPadY (core/screens.go).
const tabTitlePadY = 4

// tabInactiveInk is the opacity of an inactive tab's title.
const tabInactiveInk = 0.7

// tabBars draws the title bars of tabbed columns (domain.Scene.TabBars) with
// the tile lines: the bar in Border.Inactive, the active tab in
// Border.Active, and each title left aligned, cut with an ellipsis.
func (w *sceneWalk) tabBars() {
	if len(w.s.TabBars) == 0 {
		return
	}
	activeBg, activeInk := overlaydraw.Colors(w.s.Border.Active, "", "")
	inactiveBg, inactiveInk := overlaydraw.Colors(w.s.Border.Inactive, "", "")
	inactiveInk = fadeInk(inactiveInk, tabInactiveInk)
	for _, bar := range w.s.TabBars {
		if bar.Rect.W <= 0 || bar.Rect.H <= 0 {
			continue
		}
		full := w.physRect(bar.Rect.X, bar.Rect.Y, bar.Rect.W, bar.Rect.H)
		// Rounded like the windows (with gaps), at most a pill.
		radius := min(float32(math.Round(float64(bar.Radius)*w.scale)), float32(full.Dy())/2)
		w.fillRounded(full, w.inactive, radius)
		for _, tab := range bar.Tabs {
			rect := w.physRect(tab.Rect.X, tab.Rect.Y, tab.Rect.W, tab.Rect.H)
			bg, ink := inactiveBg, inactiveInk
			if tab.Active {
				w.fillRounded(rect, w.active, radius)
				bg, ink = activeBg, activeInk
			}
			// Core sizes the bar as the font plus tabTitlePadY above and
			// below; the raster has no padding (a tiny PadX/PadY rounds
			// to 0), the walk places it.
			font := float64(rect.Dy() - 2*w.phys(tabTitlePadY))
			w.tabTitle(tab, rect, overlaydraw.Style{FontPx: font, Background: bg, Foreground: ink,
				PadX: 0.01, PadY: 0.01, TextOnly: true, MaxWidth: rect.Dx() - 2*w.phys(tabTitlePadX)})
		}
	}
}

// tabTitle draws tab's title raster in rect (physical), vertically centred.
// The raster is cached by text and style (overlaydraw), its GPU copy by
// window, scale and style, versioned by the title's hash.
func (w *sceneWalk) tabTitle(tab domain.TabTitle, rect image.Rectangle, style overlaydraw.Style) {
	r := w.r
	if tab.Title == "" || style.MaxWidth <= 0 || style.FontPx < 1 || r.lost {
		return
	}
	pix, lw, lh := r.overlayRaster.Label(tab.Title, 0, w.scale, style)
	if lw <= 0 || lh <= 0 {
		return
	}
	x := rect.Min.X + w.phys(tabTitlePadX)
	y := rect.Min.Y + (rect.Dy()-lh)/2
	full := image.Rect(x, y, x+lw, y+lh)
	clip := full.Intersect(rect).Intersect(w.bounds)
	if clip.Empty() {
		return
	}
	key := shmKey{overlay: tabTitleOverlay | uint64(tab.Window), scale: w.scale, style: style}
	st := shmState{w: lw, h: lh, seq: labelSeq(tab.Title)}
	c := r.shmCached(key, st)
	if c == nil {
		var err error
		if c, err = r.shmCopyFor(key, st, pix, nil, 0, lw*4, nil); err != nil {
			_ = r.noteLoss(err)
			return
		}
	}
	dr := r.contentDraw(clip, full, lw, lh, [4]float32{}, 0, modeBuffer, false)
	dr.pc.color[3] = w.alpha
	dr.shm = c
	dr.pc.buf = [4]uint32{0, uint32(lw), uint32(lh), 0}
	w.draws = append(w.draws, dr)
}

// fadeInk is the premultiplied ink c at opacity a.
func fadeInk(c color.RGBA, a float64) color.RGBA {
	m := func(v uint8) uint8 { return uint8(float64(v)*a + 0.5) }
	return color.RGBA{m(c.R), m(c.G), m(c.B), m(c.A)}
}
