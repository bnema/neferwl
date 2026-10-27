package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

// overviewDivider is the inactive rule across axis at pos, inset from the
// edges of u.
func overviewDivider(axis layout.Axis, u Rect, pos int) domain.Separator {
	if axis == layout.Horizontal {
		return domain.Separator{Rect: Rect{X: pos, Y: u.Y + u.H*4/100, W: 1, H: u.H * 92 / 100}}
	}
	margin := u.W * 4 / 100
	return domain.Separator{Rect: Rect{X: u.X + margin, Y: pos, W: u.W - 2*margin, H: 1}}
}

// overviewOutline frames the selected preview with active lines of width
// b, just outside it. The selection is one preview: a selected stash card
// wins over any other focused one, which must then not be framed. A tile's
// frame is cut where the cards of the pile sit over it, so it never runs
// across the pile; a selected card keeps its complete frame. The lines are
// appended to dst, which the caller sizes for them (room for outlineLines
// past its length) so the scene's separators are allocated once.
// overviewOutline uses a rounded ring only for uncut frames. Stash cuts retain
// the square strips so the frame never draws across the cards.
func overviewOutline(dst []domain.Separator, layout []Placement, b, gap, radius int) ([]domain.Separator, *domain.SelectionOutline) {
	sel := -1
	for i, p := range layout {
		if p.Preview > 0 && p.Focused && !p.Hidden && (sel < 0 || p.Card && !layout[sel].Card) {
			sel = i
		}
	}
	if sel < 0 {
		return dst, nil
	}
	r := layout[sel].Rect
	edges := [4]Rect{
		{X: r.X - b, Y: r.Y - b, W: r.W + 2*b, H: b},
		{X: r.X - b, Y: r.Y + r.H, W: r.W + 2*b, H: b},
		{X: r.X - b, Y: r.Y, W: b, H: r.H},
		{X: r.X + r.W, Y: r.Y, W: b, H: r.H},
	}
	cut := false
	if !layout[sel].Card {
		for _, p := range layout {
			if p.Card && p.Preview > 0 && !p.Hidden {
				cut = true
				break
			}
		}
	}
	if radius := windowRadius(layout[sel], gap, radius); !cut && radius > 0 {
		return dst, &domain.SelectionOutline{Rect: Rect{X: r.X - b, Y: r.Y - b, W: r.W + 2*b, H: r.H + 2*b}, Width: b, Radius: radius + b, Active: true}
	}
	dst = slices.Grow(dst, outlineLines)
	if !cut {
		for _, e := range edges {
			dst = append(dst, domain.Separator{Rect: e, Active: true})
		}
		return dst, nil
	}
	// Stack buffers: the outline is rebuilt every overview frame.
	var cardBuf [overviewCards]Rect
	var pieceBuf [8]Rect
	cards := cardBuf[:0]
	for _, p := range layout {
		if p.Card && p.Preview > 0 && !p.Hidden {
			cards = append(cards, p.Rect)
		}
	}
	for _, e := range edges {
		for _, piece := range subtractRects(pieceBuf[:0], e, cards) {
			dst = append(dst, domain.Separator{Rect: piece, Active: true})
		}
	}
	return dst, nil
}

// subtractRects is what of r no rect of cuts covers, as rects. The edges of
// an outline are thin, so a cut splits one into at most two along its length
// (or drops it, or cuts across its thickness): the pieces are cut one rect
// at a time. The pieces are appended to dst; cuts that miss r allocate
// nothing.
func subtractRects(dst []Rect, r Rect, cuts []Rect) []Rect {
	var bufA, bufB [8]Rect
	pieces, spare := append(bufA[:0], r), bufB[:0]
	for _, c := range cuts {
		next := spare[:0]
		for _, p := range pieces {
			if !p.Overlaps(c) {
				next = append(next, p)
				continue
			}
			// The four bands of p around c: above, below, left, right.
			if c.Y > p.Y {
				next = append(next, Rect{X: p.X, Y: p.Y, W: p.W, H: c.Y - p.Y})
			}
			if c.Y+c.H < p.Y+p.H {
				next = append(next, Rect{X: p.X, Y: c.Y + c.H, W: p.W, H: p.Y + p.H - c.Y - c.H})
			}
			top, bottom := max(p.Y, c.Y), min(p.Y+p.H, c.Y+c.H)
			if c.X > p.X {
				next = append(next, Rect{X: p.X, Y: top, W: c.X - p.X, H: bottom - top})
			}
			if c.X+c.W < p.X+p.W {
				next = append(next, Rect{X: c.X + c.W, Y: top, W: p.X + p.W - c.X - c.W, H: bottom - top})
			}
		}
		// Swap the buffers: next becomes the pieces, the old pieces'
		// storage the next round's.
		pieces, spare = next, pieces
	}
	return append(dst, pieces...)
}

// overviewIn is the preview under the output-local point in layout, if
// any. Front cards take priority over peeks, including peeks appended after
// a front tile in another row's layout. The click path passes the shown
// layout, so a card is hit where it is drawn while it moves. A card that is
// nearly invisible (still fading in) is not clickable.
func overviewIn(layout []Placement, x, y float64) WindowID {
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && !p.Peek && p.Fade < overviewClickFade && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && p.Fade < overviewClickFade && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	return 0
}
