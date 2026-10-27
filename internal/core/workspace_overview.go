package core

import (
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/ports"
)

// wrapsCards reports whether the unfolded stash of w wraps round, drawn
// (unfold) and walked (stepCard). A carousel of fewer than three cards does
// not, so that each side has the card its move selects; nor does the one
// of a stash-only workspace, which has nothing beside its pile to leave it
// for. Past the ends of one that does not, a move goes on as from a lane
// (cascade) or stops (a vertical overview).
func (w *Workspace) wrapsCards() bool {
	_, k := w.stackHead()
	return k > 0 && len(w.Stash) >= 3
}

// pileWidth is the room the stash pile of w takes on the left of its row in
// a vertical overview, its margin included; 0 without a stash.
func (w *Workspace) pileWidth() int {
	if len(w.Stash) == 0 {
		return 0
	}
	card, _ := w.cardSize()
	// Never more than a third of the width: the columns keep the rest.
	a := w.overviewArea()
	return min(card+(min(len(w.Stash), overviewCards)-1)*w.cardStep()+a.W*2/100, a.W/3)
}

// cardSize is the size of a stash card in the overview.
func (w *Workspace) cardSize() (int, int) {
	r := w.stashRect()
	return int(math.Round(float64(r.W) * overviewCardZoom)), int(math.Round(float64(r.H) * overviewCardZoom))
}

// pileBand is the height a horizontal overview gives the stash of w under
// its selected lane: the spacing, then the folded pile with its cards
// behind. 0 without a stash, or in a vertical overview, where the pile
// sits left of the row. The lane and the band are centred together.
func (w *Workspace) pileBand(axis layout.Axis) int {
	if axis != layout.Horizontal || len(w.Stash) == 0 {
		return 0
	}
	_, ch := w.cardSize()
	return max(1, w.overviewArea().H*3/100) + ch + (min(len(w.Stash), overviewCards)-1)*w.cardStep()
}

// cardStep is the offset between stacked cards, smaller on tiny outputs.
func (w *Workspace) cardStep() int {
	a := w.overviewArea()
	return min(overviewCardStep, a.W/100, a.H/50)
}

// pile appends the stash of w to dst as cards in its slot along axis (see
// the overview's stash comment). With entry at selected (at >= 0, on the
// current row: dim false) the pile unfolds; else it is folded: the stash
// selection in front, painted last, up to overviewCards-1 next ones
// (wrapping round) behind it, farthest first, dimmed; the rest hidden.
func (w *Workspace) pile(dst []Placement, axis layout.Axis, slot Rect, dim bool, at int) []Placement {
	n := len(w.Stash)
	if n == 0 {
		return dst
	}
	u := w.overviewArea()
	cw, ch := w.cardSize()
	rowH := int(math.Round(float64(w.Usable.H) * w.overviewZoom(axis)))
	var x0, y0 int
	if axis == layout.Horizontal {
		// Centred on the row, under the selected lane: slot is the
		// lane's, the pile band (pileBand) taken off its height, so the
		// band starts below the lane. The cards behind rise from the
		// front one, which goes at the bottom of the band. Both scale
		// with the area (the lane at most overviewMaxZoom of it), so
		// the pile stays inside whatever its size.
		x0 = slot.X + (slot.W-cw)/2
		y0 = slot.Y + (slot.H-rowH)/2 + rowH + w.pileBand(axis) - ch
	} else {
		// Stack rows reserve two peek steps above the front card. Align
		// the stash with that front band, not with the top of its envelope.
		band := w.frontSlot(axis, slot)
		x0, y0 = band.X+u.W/100, band.Y+(rowH-ch)/2
	}
	folded := Rect{X: x0, Y: y0, W: cw, H: ch}
	if at >= 0 && !dim {
		return w.unfold(dst, axis, folded, min(at, n-1))
	}
	front, behind, step := min(max(w.stashAt, 0), n-1), min(n, overviewCards)-1, w.cardStep()
	card := func(i, d int) Placement {
		p := Placement{ID: w.Stash[i].ID, Rect: folded, Preview: overviewCardZoom, Card: true}
		p.Rect.X, p.Rect.Y = p.Rect.X+d*step, p.Rect.Y-d*step
		if d > 0 || dim {
			p.Peeking(1)
		}
		return p
	}
	for d := behind; d >= 0; d-- {
		dst = append(dst, card((front+d)%n, d))
	}
	for i := range w.Stash {
		// The cards neither in front nor behind it: hidden, so their
		// clients are not left showing.
		if (i-front+n)%n > behind {
			dst = append(dst, Placement{ID: w.Stash[i].ID, Rect: folded, Card: true, Hidden: true})
		}
	}
	return dst
}

// unfold lays the stash of w out from the folded pile's card rect as a
// carousel along axis: a row in a horizontal overview, a column in a
// vertical one. The selected card at sits on the pile, in the middle; the
// cards before it come on the left (above), the ones after it on the right
// (below), in stash order, wrapping round (wrapsCards) with the oldest
// following the newest (carouselSlot). A wider gap marks that seam. A
// carousel that fits the overview area is shifted into it. Longer, one
// that wraps keeps the selection in the middle; one that does not stops at
// the area's edges, like a wide row. The cards past the area are hidden,
// the ones other than at dimmed. The cards are appended to dst.
func (w *Workspace) unfold(dst []Placement, axis layout.Axis, folded Rect, at int) []Placement {
	n := len(w.Stash)
	u := w.overviewArea()
	size, origin, lo, room := folded.W, folded.X, u.X, u.W
	if axis == layout.Vertical {
		size, origin, lo, room = folded.H, folded.Y, u.Y, u.H
	}
	stride := size + 2*w.cardStep()
	// The extra room at the seam, on top of the stride.
	gap := size / 4
	wraps := w.wrapsCards()
	pos := func(s int) int {
		p := origin + s*stride
		switch {
		case s > 0 && at+s >= n:
			p += gap
		case s < 0 && at+s < 0:
			p -= gap
		}
		return p
	}
	// The slots of the ends.
	from, to := -at, n-1-at
	if wraps {
		from, to = -(n / 2), (n-1)/2
	}
	first, total := pos(from), pos(to)+size-pos(from)
	var shift int
	switch {
	case total <= room:
		shift = min(max(first, lo), lo+room-total) - first
	case !wraps:
		shift = min(max(first, lo+room-total), lo) - first
	}
	for i := range w.Stash {
		r := folded
		if p := pos(carouselSlot(i, at, n, wraps)) + shift; axis == layout.Vertical {
			r.Y = p
		} else {
			r.X = p
		}
		dst = w.unfoldedCard(dst, i, r, at, u)
	}
	return dst
}

// unfoldedCard appends stash card i of an unfolded pile at r to dst: framed
// when it is the selected one at, dimmed otherwise, hidden out of the area u.
func (w *Workspace) unfoldedCard(dst []Placement, i int, r Rect, at int, u Rect) []Placement {
	p := Placement{ID: w.Stash[i].ID, Rect: r, Preview: overviewCardZoom, Card: true, Focused: i == at}
	if i != at {
		p.Peeking(1)
	}
	if !r.Overlaps(u) {
		p.Hidden, p.Preview, p.Focused = true, 0, false
	}
	return append(dst, p)
}

// overviewZoom is the scale that fits every column of w in the usable
// width, within the overview bounds, in an overview along axis: only a
// vertical one sets the stash pile beside the row.
func (w *Workspace) overviewZoom(axis layout.Axis) float64 {
	z, _ := w.zoomSpan(axis)
	return z
}

// zoomSpan is overviewZoom with the unscaled span of the tiles it fits. A
// cascade workspace spans one screen whatever its band count.
func (w *Workspace) zoomSpan(axis layout.Axis) (float64, int) {
	// Only the span is read: the tiles go in the zoom scratch, dead on return.
	var span int
	w.zoomBuf, span, _ = w.previewTilesInto(w.zoomBuf[:0])
	if span <= 0 {
		return overviewMaxZoom, span
	}
	room := float64(w.overviewArea().W) * 0.96
	if axis == layout.Vertical {
		room = float64(w.overviewArea().W-w.pileWidth()) * 0.96
	}
	z := min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
	if w.stacked() {
		// Reserve two clear peeks on either side of the front row.
		z = min(z, overviewMaxZoom/(1+4*overviewPeekStep))
	}
	return z, span
}

// rowWidth is the width of the slot of w in a horizontal overview: what its
// tiles take (at most the area), plus the room for the covering floats
// peeking on either side of the front card.
func (w *Workspace) rowWidth(axis layout.Axis) int {
	z, span := w.zoomSpan(axis)
	if span <= 0 {
		// An empty workspace keeps the room of a screen.
		span = w.Usable.W
	}
	width := min(int(math.Round(float64(span)*z)), w.overviewArea().W)
	if w.stacked() {
		width += 4 * w.peekStep(axis)
	}
	return width
}

// peekStep is the shared step for a stack row and its front-card inset.
func (w *Workspace) peekStep(axis layout.Axis) int {
	return max(1, int(math.Round(float64(w.Usable.H)*w.overviewZoom(axis)*overviewPeekStep)))
}

// frontSlot is the part of slot the front card of w takes: slot shifted
// along axis past the room reserved for two cards peeking before it, and
// (along a horizontal axis, where the slot is as wide as its cards) without
// the room of the two after it. A single-card row keeps slot.
func (w *Workspace) frontSlot(axis layout.Axis, slot Rect) Rect {
	if w.stacked() {
		p := w.peekStep(axis)
		axis.Offset(&slot, 2*p)
		if axis == layout.Horizontal {
			slot.W -= 4 * p
		}
	}
	return slot
}

// rowHeight includes the largest possible stack envelope (two peeks above
// and two below). Neighbor rows use the same measure as the current row.
func (w *Workspace) rowHeight(axis layout.Axis) int {
	h := int(math.Round(float64(w.Usable.H) * w.overviewZoom(axis)))
	if w.stacked() {
		h += 4 * w.peekStep(axis)
	}
	return h
}

// previewTiles lays tiles out unscrolled, relative to the usable area's
// corner. Fixed overflow uses its on-screen geometry; scroll overflow uses
// an unscrolled row. span is the row's width; sel is the focused column.
func (w *Workspace) previewTiles() (tiles []Placement, span int, sel Rect) {
	return w.previewTilesInto(make([]Placement, 0, w.columnWindows()))
}

// previewTilesInto is previewTiles appending to tiles (dst[:0] to reuse its
// storage: the result then aliases it). Geometry and row rects share
// disjoint regions of w.rowBuf, dead when it returns.
func (w *Workspace) previewTilesInto(tiles []Placement) ([]Placement, int, Rect) {
	// Reserve both geometry and row scratch without retaining the snapshot.
	rows := 0
	for _, c := range w.Columns {
		rows = max(rows, len(c.Windows))
	}
	need := len(w.Columns) + rows
	if cap(w.rowBuf) < need {
		w.rowBuf = make([]Rect, 0, need)
	}
	return w.Engine().PreviewTiles(w.Snapshot(), tiles, w.rowBuf)
}

// previewRow scales the tiles of w into a row placed in slot, centred
// in the slot's width, or scrolled so the focused column shows when the
// row is wider (right of the stash pile in a vertical overview). Dimmed rows (neighbor
// workspaces) have no focus; neither has a row whose pile has it (lit
// false).
func (w *Workspace) previewRow(axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	tiles, span, sel := w.previewTiles()
	// The neighbor layout's lane encoding, the current workspace's overview axis.
	return w.previewRowTiles(axis, slot, dim, lit, w.Engine().PreviewLanes(), tiles, span, sel)
}

// previewRowTiles scales tiles, laid out unscrolled over span with sel the
// focused one, into slot of an overview along axis. Placement stays inside
// slot.
//
// With lanes set (the tiles of a cascade workspace), the tiles carry their
// band in Y, one Usable.H apart. The band of sel is the selected lane,
// centred across the axis; the other bands sit a lane away from it, dimmed,
// and are hidden when they leave the area. A workspace whose lanes run along
// the overview axis shows only its selected lane. In a horizontal overview,
// tiles entirely outside the area are hidden.
func (w *Workspace) previewRowTiles(axis layout.Axis, slot Rect, dim, lit, lanes bool, tiles []Placement, span int, sel Rect) []Placement {
	z := w.overviewZoom(axis)
	scale := func(v int) int { return int(math.Round(float64(v) * z)) }
	width := scale(span)
	u := w.overviewArea()
	top := slot.Y
	if axis == layout.Horizontal {
		top = slot.Y + (slot.H-scale(w.Usable.H))/2
	}
	// Rows stay centred on the output like rows without a stash; in a
	// vertical overview the pile only pushes a row right when they would
	// overlap. A horizontal one gives the pile a band under the selected
	// lane (pileBand).
	left := 0
	if axis == layout.Vertical {
		left = w.pileWidth()
	}
	x := max(slot.X+(slot.W-width)/2, slot.X+left)
	ux, uw := slot.X+left, slot.W-left
	if width > uw {
		x = ux + uw/2 - scale(sel.X+sel.W/2)
		x = min(max(x, ux+uw-width), ux)
	}
	content, _ := w.Engine().Axes()
	stride := content.Span(w.Usable)
	lanes = lanes && stride > 0
	// Lanes along the overview axis would overlap the neighbor workspaces.
	otherLanes := content != axis
	laneStep := scale(stride) + max(1, u.H*3/100)
	// The lanes below the selected one leave room for the stash pile.
	below := w.pileBand(axis)
	selLane := 0
	if lanes {
		selLane = max(sel.Y, 0) / stride
	}
	for i := range tiles {
		if tiles[i].Hidden {
			continue
		}
		r := tiles[i].Rect
		lane, ly := 0, r.Y
		if lanes {
			l := r.Y / stride
			lane, ly = l-selLane, r.Y-l*stride
		}
		x0, y0 := scale(r.X), scale(ly)
		y := top + y0 + lane*laneStep
		if lanes && lane > 0 {
			y += below
		}
		tiles[i].Rect = Rect{X: x + x0, Y: y, W: scale(r.X+r.W) - x0, H: scale(ly+r.H) - y0}
		tiles[i].Preview = z
		if dim || lane != 0 {
			tiles[i].Peeking(1)
		}
		tiles[i].Focused = tiles[i].Focused && !dim && lit && lane == 0
		out := false
		if axis == layout.Horizontal {
			// A row wider than its slot scrolls: what leaves the slot
			// would sit on the neighbor workspaces.
			out = tiles[i].Rect.X < ux || tiles[i].Rect.X+tiles[i].Rect.W > ux+uw
		}
		if lane != 0 && !otherLanes || out || (lanes || axis == layout.Horizontal) && !tiles[i].Rect.Overlaps(u) {
			tiles[i].Hidden, tiles[i].Preview, tiles[i].Focused = true, 0, false
		}
	}
	return tiles
}

// rowSnapshot is a row as the overview first showed it: browsing moves its
// focus and scroll, closing puts them back.
type rowSnapshot struct {
	focus     WindowID
	floats    []Float
	fullWidth WindowID
	// FullWidth history is restored when active binds changed it.
	maximized []WindowID
}

// restoreRow puts w back as snapshot s found it.
func restoreRow(w *Workspace, s rowSnapshot) {
	if s.focus != 0 {
		w.FocusID(s.focus)
	} else {
		w.floatFocus, w.stashFocus = false, false
	}
	// FocusID on a float raises it; restore the initial order and below
	// flags for floats still present, leaving new floats in their order.
	current := w.Floats
	w.Floats = nil
	for _, f := range s.floats {
		for i, now := range current {
			if now.ID == f.ID {
				now.below = f.below
				w.Floats = append(w.Floats, now)
				current = append(current[:i], current[i+1:]...)
				break
			}
		}
	}
	w.Floats = append(w.Floats, current...)
	// Fixed overflow maximizes at most one column: only the one holding
	// the anchor window, wherever active binds moved it. Scroll overflow
	// keeps each column's own width.
	if w.Engine().MaximizeHides() {
		w.Unmaximize()
		for i := range w.Columns {
			if s.fullWidth != 0 && slices.Contains(w.Columns[i].Windows, s.fullWidth) {
				w.Maximize(i)
				break
			}
		}
	}
	// Windows that left the row since leave its maximize history too.
	w.Maximized = slices.DeleteFunc(slices.Clone(s.maximized), func(id WindowID) bool { return !w.has(id) })
	w.scroll()
}

// neighborSlot is the slot of w, the neighbor before or after the current
// slot cs along axis, spacing away; gapAt is where the spacing begins.
func neighborSlot(axis layout.Axis, u, cs Rect, w *Workspace, before bool, spacing int) (slot Rect, gapAt int) {
	if axis == layout.Horizontal {
		slot = Rect{Y: u.Y, W: w.rowWidth(axis), H: u.H}
		if before {
			gapAt = cs.X - spacing
			slot.X = gapAt - slot.W
		} else {
			gapAt = cs.X + cs.W
			slot.X = gapAt + spacing
		}
		return slot, gapAt
	}
	h := w.rowHeight(axis)
	slot = Rect{X: u.X, W: u.W, H: h}
	if before {
		gapAt = cs.Y - spacing
		slot.Y = gapAt - h
	} else {
		gapAt = cs.Y + cs.H
		slot.Y = gapAt + spacing
	}
	return slot, gapAt
}

// overviewDivider is the inactive rule across axis at pos, inset from the
// edges of u.
func overviewDivider(axis layout.Axis, u Rect, pos int) ports.Separator {
	if axis == layout.Horizontal {
		return ports.Separator{Rect: Rect{X: pos, Y: u.Y + u.H*4/100, W: 1, H: u.H * 92 / 100}}
	}
	margin := u.W * 4 / 100
	return ports.Separator{Rect: Rect{X: u.X + margin, Y: pos, W: u.W - 2*margin, H: 1}}
}

// fan places front and neighboring cards, farthest visible first and front
// last. A card may contain several tiles; omitted cards stay in the layout
// hidden so their clients are not left showing on screen.
func fan(cards [][]Placement, front, maxBehind, maxBefore int,
	behind, before func(int) (int, int), dim, lit bool) []Placement {
	n := len(cards)
	if n == 0 {
		return nil
	}
	front = min(max(front, 0), n-1)
	out := make([]Placement, 0, n)
	add := func(i, distance int, offset func(int) (int, int), peek bool) {
		dx, dy := offset(distance)
		for _, p := range cards[i] {
			if !p.Hidden {
				p.Rect.X += dx
				p.Rect.Y += dy
				if p.Peek || peek || dim {
					p.Peeking(1)
				}
				p.Focused = p.Focused && !peek && !dim && lit
			}
			out = append(out, p)
		}
	}
	shown := make([]bool, n)
	// Behind and before are in nearest-first order; paint farthest first.
	for d := min(maxBehind, n-1); d >= 1; d-- {
		i := front + d
		if i >= n {
			continue
		}
		add(i, d, behind, true)
		shown[i] = true
	}
	for d := min(maxBefore, n-1); d >= 1; d-- {
		i := front - d
		if i < 0 || shown[i] {
			continue
		}
		add(i, d, before, true)
		shown[i] = true
	}
	add(front, 0, behind, false)
	shown[front] = true
	for i, card := range cards {
		if shown[i] {
			continue
		}
		for _, p := range card {
			p.Hidden = true
			p.Preview = 0
			p.Focused = false
			out = append(out, p)
		}
	}
	return out
}

// carouselSlot is the place of stash card i in a carousel of n cards
// centred on card at: 0 for at, negative on the left (above), positive on
// the right (below). Wrapping round, the left side takes the extra card of
// an even count; else the cards keep the stash order.
func carouselSlot(i, at, n int, wraps bool) int {
	if !wraps {
		return i - at
	}
	s := (i - at + n) % n
	if s > (n-1)/2 {
		s -= n
	}
	return s
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
func overviewOutline(dst []ports.Separator, layout []Placement, b, gap, radius int) ([]ports.Separator, *ports.SelectionOutline) {
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
		return dst, &ports.SelectionOutline{Rect: Rect{X: r.X - b, Y: r.Y - b, W: r.W + 2*b, H: r.H + 2*b}, Width: b, Radius: radius + b, Active: true}
	}
	dst = slices.Grow(dst, outlineLines)
	if !cut {
		for _, e := range edges {
			dst = append(dst, ports.Separator{Rect: e, Active: true})
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
			dst = append(dst, ports.Separator{Rect: piece, Active: true})
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
