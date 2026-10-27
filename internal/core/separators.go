package core

import (
	"github.com/bnema/neferwl/internal/domain"
)

// Two modes. Without gaps (gap 0), borders are tmux-style lines (like tmux
// screen-redraw.c): lines separate visible panes, never outline a lone tile
// or a fullscreen/zoomed tile. Adjacent tiles share one line, owned (and
// inset) by the left/top tile; focus lights only the adjacent sides of the
// focused tile, including corners. With exactly two visible tiles, each
// lights its own half of the shared line (left/top first). With gaps, each
// tile owns a full four-sided border, like a float, and lights it whole,
// even when alone. Hidden, scrolled-off, under-panel, other-workspace and
// other-output tiles do not count, nor do tiles that share no line. Lines
// at the edges of the usable area (output or panels) are omitted. Floating
// windows also get their own full border, drawn with that window
// (Separator.Window), even when it is the only visible window. Only the
// focused output's focused window can light lines; keyboard grabs by popups
// and layers do not change the underlying window's visual focus.

var sides = [...]domain.Sides{domain.SideLeft, domain.SideRight, domain.SideTop, domain.SideBottom}

// separators are the lines of the windows seen in v, each clipped to its
// window's area, inactive ones first so active ones draw over them. lit
// says whether the focused tile shows focus (only on the focused output).
func separators(ps []Placement, width, gap int, v viewport, lit bool) []domain.Separator {
	if width <= 0 {
		return nil
	}
	o := v.frame
	var out []domain.Separator
	add := func(p *Placement, r Rect, active bool) {
		// A line cannot belong to a scrolled-off window, bleed onto
		// another output or, for a tile, over a panel, even when a
		// neighboring tile remains visible.
		a := v.area(*p)
		x0, y0 := max(r.X, a.X), max(r.Y, a.Y)
		x1, y1 := min(r.X+r.W, a.X+a.W), min(r.Y+r.H, a.Y+a.H)
		if x0 >= x1 || y0 >= y1 {
			return
		}
		r = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
		var id WindowID
		if p.Floating || gap > 0 {
			id = p.ID
		}
		if out == nil {
			// One line per side of every tile, and the focused one's four
			// lit lines: the common case grows no further.
			out = make([]domain.Separator, 0, 4*len(ps)+4)
		}
		out = append(out, domain.Separator{Rect: r, Active: active, Window: id})
	}
	var focused *Placement
	tiles := 0
	for i := range ps {
		p := &ps[i]
		// A leaving float keeps its own lines while it fades; a leaving
		// tile's were shared with its neighbours, which re-flowed.
		if p.Fullscreen || !(v.shows(*p) || p.Leaving && p.Floating && p.Rect.Overlaps(o)) {
			continue
		}
		if gap > 0 {
			for _, s := range sides {
				add(p, strip(p.Rect, s, 0, width), p.Focused && lit)
			}
			continue
		}
		// Tiles sharing a line: Neighbors only counts tiles visible in
		// the usable area, not those under a panel.
		if !p.Floating && p.Neighbors != 0 {
			tiles++
		}
		for _, s := range sides {
			if p.Inset&s != 0 {
				add(p, strip(p.Rect, s, 0, width), false)
			}
		}
		if p.Focused && lit {
			focused = p
		}
	}
	if focused == nil {
		return out
	}
	f := focused
	for _, s := range sides {
		var line domain.Rect
		switch {
		case f.Neighbors&s != 0 && f.Inset&s != 0:
			line = strip(f.Rect, s, 0, width)
		case f.Neighbors&s != 0:
			line = strip(f.Rect, s, width, width)
		case f.Inset&s != 0:
			// A float's own border.
			add(f, strip(f.Rect, s, 0, width), true)
			continue
		default:
			continue
		}
		line = closeCorners(line, *f, s, width)
		// With gaps the loop above continued, so two tiles share one line.
		if tiles == 2 {
			line = half(line, s)
		}
		add(f, line, true)
	}
	return out
}

// strip is the b-wide strip along side s of r: inside r when out is 0,
// else just outside it (the neighbor's line).
func strip(r Rect, s domain.Sides, out, b int) Rect {
	b = min(b, max(r.W/2, out), max(r.H/2, out))
	switch s {
	case domain.SideLeft:
		return Rect{X: r.X - out, Y: r.Y, W: b, H: r.H}
	case domain.SideRight:
		return Rect{X: r.X + r.W - b + out, Y: r.Y, W: b, H: r.H}
	case domain.SideTop:
		return Rect{X: r.X, Y: r.Y - out, W: r.W, H: b}
	}
	return Rect{X: r.X, Y: r.Y + r.H - b + out, W: r.W, H: b}
}

// closeCorners runs a line along side s of p b further at each end where
// the crossing line lies outside p (a neighbor's), so lit lines meet.
func closeCorners(line Rect, p Placement, s domain.Sides, b int) Rect {
	outside := func(s domain.Sides) bool { return p.Neighbors&s != 0 && p.Inset&s == 0 }
	if s == domain.SideLeft || s == domain.SideRight {
		if outside(domain.SideTop) {
			line.Y, line.H = line.Y-b, line.H+b
		}
		if outside(domain.SideBottom) {
			line.H += b
		}
		return line
	}
	if outside(domain.SideLeft) {
		line.X, line.W = line.X-b, line.W+b
	}
	if outside(domain.SideRight) {
		line.W += b
	}
	return line
}

// half keeps the half of a line owned by the tile on side s of it: the
// first half for the left or top tile, else the second.
func half(r Rect, s domain.Sides) Rect {
	switch s {
	case domain.SideRight:
		r.H /= 2
	case domain.SideLeft:
		r.Y, r.H = r.Y+r.H/2, r.H-r.H/2
	case domain.SideBottom:
		r.W /= 2
	case domain.SideTop:
		r.X, r.W = r.X+r.W/2, r.W-r.W/2
	}
	return r
}

// windowRadius is the corner radius of p: floats always, tiles only with
// gaps, never fullscreen windows.
func windowRadius(p Placement, gap, radius int) int {
	if !p.Fullscreen && (p.Floating || gap > 0) {
		return radius
	}
	return 0
}
