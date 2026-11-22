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
	fill := tabBarFill(w.inactive, w.bg, w.active)
	activeBg, inactiveBg := rgba(w.active), rgba(fill)
	activeInk, inactiveInk := overlaydraw.InkFor(activeBg), overlaydraw.MutedInk(inactiveBg)
	for _, bar := range w.s.TabBars {
		if bar.Rect.W <= 0 || bar.Rect.H <= 0 {
			continue
		}
		// Inside the visible tab's ring, like a window's content.
		in := min(w.tabBarInset(&bar), bar.Rect.W/2, bar.Rect.H)
		full := w.physRect(bar.Rect.X+in, bar.Rect.Y+in, bar.Rect.W-2*in, bar.Rect.H-in)
		if full.Empty() {
			continue
		}
		// The visible tab's title bar: top corners rounded like the
		// windows (with gaps) within the ring, at most the bar height;
		// bottom square.
		radius := float32(math.Round(float64(bar.Radius) * w.scale))
		if in > 0 && radius > 0 {
			bPhys := min(full.Min.X-w.phys(bar.Rect.X), full.Min.Y-w.phys(bar.Rect.Y))
			radius = max(radius-float32(max(bPhys, 0)), 0)
		}
		radius = min(radius, float32(full.Dy()))
		w.square = cornersBottom
		w.fillRounded(full, fill, radius)
		for _, tab := range bar.Tabs {
			rect := w.physRect(tab.Rect.X, tab.Rect.Y, tab.Rect.W, tab.Rect.H).Intersect(full)
			bg, ink := inactiveBg, inactiveInk
			if tab.Active {
				// Clipped to the bar's shape: rounded where it reaches
				// a bar corner, however narrow.
				if dr := w.fillRounded(rect, w.active, radius); dr != nil {
					roundedDraw(dr, full, radius, cornersBottom)
				}
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

// tabBarBlend is the share of Border.Active in a bar fill that would
// otherwise vanish into the background.
const tabBarBlend = 0.25

// tabBarFill is the bar colour: Border.Inactive, or, when that is (nearly)
// the output background, Border.Active blended over the background at
// tabBarBlend, so the bar stays visible.
func tabBarFill(inactive, bg, active [3]uint8) [3]uint8 {
	near := true
	for i := range inactive {
		if d := int(inactive[i]) - int(bg[i]); d > 4 || d < -4 {
			near = false
		}
	}
	if !near {
		return inactive
	}
	var c [3]uint8
	for i := range c {
		c[i] = uint8(math.Round(tabBarBlend*float64(active[i]) + (1-tabBarBlend)*float64(bg[i])))
	}
	return c
}

// tabRing is the rect window id or a flash (id 0) at r (logical)
// outlines: for a visible tab, the union of r and its bar (one entity, one
// ring); ok false, r otherwise. A window is a visible tab when it sits
// right under a bar whose active tab it is; a flash, which names no
// window, by geometry alone.
func tabRing(bars []domain.TabBar, id domain.WindowID, r domain.Rect) (domain.Rect, bool) {
	for i := range bars {
		b := &bars[i].Rect
		if r.Y != b.Y+b.H || r.X != b.X || r.W != b.W {
			continue
		}
		if id != 0 && !activeTab(&bars[i], id) {
			continue
		}
		return domain.Rect{X: r.X, Y: b.Y, W: r.W, H: r.Y + r.H - b.Y}, true
	}
	return r, false
}

// activeTab reports whether id is bar's active tab.
func activeTab(bar *domain.TabBar, id domain.WindowID) bool {
	for _, tab := range bar.Tabs {
		if tab.Active {
			return tab.Window == id
		}
	}
	return false
}

// tabBarInset is the bar's room inside its visible tab's ring: the
// border width when that window draws its own ring (gaps), else 0.
func (w *sceneWalk) tabBarInset(bar *domain.TabBar) int {
	for _, tab := range bar.Tabs {
		if tab.Active && w.ringColor(tab.Window) != "" {
			return max(w.s.Border.Width, 0)
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
	st := shmState{w: lw, h: lh, seq: labelSeq(tab.Title), windowSeq: w.gen}
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

// tabSeparators is separators for a visible tab at r without radius: its
// lines grown to enclose its bar, u (tabRing): the top line moves to the
// bar's top, the side lines reach it.
func (w *sceneWalk) tabSeparators(id domain.WindowID, r, u domain.Rect) {
	for _, sep := range w.s.Separators {
		if sep.Window != id {
			continue
		}
		col, color := w.s.Border.Inactive, w.inactive
		if sep.Active {
			col, color = w.s.Border.Active, w.active
		}
		if col == "" {
			continue
		}
		s := sep.Rect
		if s.Y == r.Y {
			if s.H < r.H && s.W > s.H {
				s.Y = u.Y // the top line
			} else {
				s.Y, s.H = u.Y, s.H+r.Y-u.Y
			}
		}
		w.fill(w.physRect(s.X, s.Y, s.W, s.H), color)
	}
}
