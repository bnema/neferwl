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

// tabBars draws the title bars of tabbed columns (domain.Scene.TabBars) with
// the tile lines: the bar in Border.Inactive, the active tab in
// Border.Active, and each title left aligned, cut with an ellipsis, in an
// opaque ink contrasting with what it is drawn on (muted when inactive).
func (w *sceneWalk) tabBars() {
	if len(w.s.TabBars) == 0 {
		return
	}
	rgba := func(c [3]uint8) color.RGBA { return color.RGBA{c[0], c[1], c[2], 255} }
	activeBg, inactiveBg := rgba(w.active), rgba(w.inactive)
	activeInk, inactiveInk := overlaydraw.InkFor(activeBg), overlaydraw.MutedInk(inactiveBg)
	for _, bar := range w.s.TabBars {
		if bar.Rect.W <= 0 || bar.Rect.H <= 0 {
			continue
		}
		full := w.physRect(bar.Rect.X, bar.Rect.Y, bar.Rect.W, bar.Rect.H)
		// The visible tab's title bar: top corners rounded like the
		// windows (with gaps), at most the bar height; bottom square.
		radius := min(float32(math.Round(float64(bar.Radius)*w.scale)), float32(full.Dy()))
		w.square = cornersBottom
		w.fillRounded(full, w.inactive, radius)
		for i, tab := range bar.Tabs {
			rect := w.physRect(tab.Rect.X, tab.Rect.Y, tab.Rect.W, tab.Rect.H)
			bg, ink := inactiveBg, inactiveInk
			if tab.Active {
				// Rounded only on the corners it shares with the bar.
				w.square = cornersBottom | cornersTop
				if i == 0 {
					w.square &^= cornerTopLeft
				}
				if i == len(bar.Tabs)-1 {
					w.square &^= cornerTopRight
				}
				w.fillRounded(rect, w.active, radius)
				bg, ink = activeBg, activeInk
			}
			// Sizes come from logical sizes, not the rounded physical
			// rect, so a moving bar keeps its raster. The raster has no
			// padding (a tiny PadX/PadY rounds to 0), the walk places it.
			maxW := int(float64(tab.Rect.W-2*tabTitlePadX) * w.scale)
			w.tabTitle(tab, rect, overlaydraw.Style{FontPx: bar.FontPx * w.scale, Background: bg, Foreground: ink,
				PadX: 0.01, PadY: 0.01, TextOnly: true, MaxWidth: maxW})
		}
	}
	w.square = 0
}

// tabSquare is the square-corner mask of a window or flash at r (logical):
// a visible tab's top corners sit under its bar (domain.TabBar), square.
func (w *sceneWalk) tabSquare(r domain.Rect) uint32 {
	for i := range w.s.TabBars {
		b := &w.s.TabBars[i].Rect
		if gap := r.Y - b.Y - b.H; r.X == b.X && r.W == b.W && gap >= 0 && gap <= domain.TabBarGap {
			return cornersTop
		}
	}
	return 0
}

// tabTitle draws tab's title raster in rect (physical), vertically centred.
// The raster is cached per window (overlaydraw.Titles), its GPU copy by
// window, scale and style without MaxWidth, versioned by the title's hash
// and the raster size: a width change that cuts the title the same way
// keeps the copy.
func (w *sceneWalk) tabTitle(tab domain.TabTitle, rect image.Rectangle, style overlaydraw.Style) {
	r := w.r
	if tab.Title == "" || style.MaxWidth <= 0 || style.FontPx < 1 || r.lost {
		return
	}
	pix, lw, lh := r.tabTitles.Title(uint64(tab.Window), tab.Title, style, r.frame)
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
	keyStyle := style
	keyStyle.MaxWidth = 0
	key := shmKey{overlay: tabTitleOverlay | uint64(tab.Window), scale: w.scale, style: keyStyle}
	st := shmState{w: lw, h: lh, seq: labelSeq(tab.Title), windowSeq: overlaydraw.FontGen()}
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
