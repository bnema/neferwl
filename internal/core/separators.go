package core

import "github.com/bnema/neferwl/internal/ports"

// Like tmux screen-redraw.c, lines separate visible panes, never outline a
// lone tile or a fullscreen/zoomed tile. Adjacent tiles share one line,
// owned (and inset) by the left/top tile; focus lights only the adjacent
// sides of the focused tile, including corners. With exactly two visible
// tiles and no gaps, each lights its own half of the shared line (left/top
// first). With gaps, each tile owns its neighbor-facing lines and lights
// them whole. Hidden, scrolled-off, under-panel, other-workspace and
// other-output tiles do not count, nor do tiles that share no line. Lines
// at the edges of the usable area (output or panels) are omitted. Floating windows differ from tmux tiles:
// each gets its own full border, drawn with that window (Separator.Window),
// even when it is the only visible window. Only the
// focused output's focused window can light lines; keyboard grabs by popups
// and layers do not change the underlying window's visual focus.

var sides = [...]ports.Sides{ports.SideLeft, ports.SideRight, ports.SideTop, ports.SideBottom}

// separators are the lines of a layout on output o, inactive ones first
// so active ones draw over them. lit says whether the focused tile shows
// focus (only on the focused output).
func separators(ps []Placement, width, gap int, o Rect, lit bool) []ports.Separator {
	if width <= 0 {
		return nil
	}
	var out []ports.Separator
	add := func(p *Placement, r Rect, active bool) {
		// A line cannot belong to a scrolled-off window or bleed onto
		// another output, even when a neighboring tile remains visible.
		x0, y0 := max(r.X, o.X), max(r.Y, o.Y)
		x1, y1 := min(r.X+r.W, o.X+o.W), min(r.Y+r.H, o.Y+o.H)
		if x0 >= x1 || y0 >= y1 {
			return
		}
		r = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
		var id WindowID
		if p.Floating {
			id = p.ID
		}
		if out == nil {
			// One line per side of every tile, and the focused one's four
			// lit lines: the common case grows no further.
			out = make([]ports.Separator, 0, 4*len(ps)+4)
		}
		out = append(out, ports.Separator{Rect: r, Active: active, Window: id})
	}
	var focused *Placement
	tiles := 0
	for i := range ps {
		p := &ps[i]
		// A leaving float keeps its own lines while it fades; a leaving
		// tile's were shared with its neighbours, which re-flowed.
		if p.Fullscreen || !(onScreen(*p, o) || p.Leaving && p.Floating && p.Rect.Overlaps(o)) {
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
		var line ports.Rect
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
		// Two tiles share one line only without gaps.
		if tiles == 2 && gap == 0 {
			line = half(line, s)
		}
		add(f, line, true)
	}
	return out
}

// strip is the b-wide strip along side s of r: inside r when out is 0,
// else just outside it (the neighbor's line).
func strip(r Rect, s ports.Sides, out, b int) Rect {
	b = min(b, max(r.W/2, out), max(r.H/2, out))
	switch s {
	case ports.SideLeft:
		return Rect{X: r.X - out, Y: r.Y, W: b, H: r.H}
	case ports.SideRight:
		return Rect{X: r.X + r.W - b + out, Y: r.Y, W: b, H: r.H}
	case ports.SideTop:
		return Rect{X: r.X, Y: r.Y - out, W: r.W, H: b}
	}
	return Rect{X: r.X, Y: r.Y + r.H - b + out, W: r.W, H: b}
}

// closeCorners runs a line along side s of p b further at each end where
// the crossing line lies outside p (a neighbor's), so lit lines meet.
func closeCorners(line Rect, p Placement, s ports.Sides, b int) Rect {
	outside := func(s ports.Sides) bool { return p.Neighbors&s != 0 && p.Inset&s == 0 }
	if s == ports.SideLeft || s == ports.SideRight {
		if outside(ports.SideTop) {
			line.Y, line.H = line.Y-b, line.H+b
		}
		if outside(ports.SideBottom) {
			line.H += b
		}
		return line
	}
	if outside(ports.SideLeft) {
		line.X, line.W = line.X-b, line.W+b
	}
	if outside(ports.SideRight) {
		line.W += b
	}
	return line
}

// half keeps the half of a line owned by the tile on side s of it: the
// first half for the left or top tile, else the second.
func half(r Rect, s ports.Sides) Rect {
	switch s {
	case ports.SideRight:
		r.H /= 2
	case ports.SideLeft:
		r.Y, r.H = r.Y+r.H/2, r.H-r.H/2
	case ports.SideBottom:
		r.W /= 2
	case ports.SideTop:
		r.X, r.W = r.X+r.W/2, r.W-r.W/2
	}
	return r
}
