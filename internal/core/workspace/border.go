package workspace

import (
	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/core/layout"
)

// BorderKind identifies a resize edge between columns or rows.
type BorderKind int

const (
	// BorderColumn resizes adjacent column widths.
	BorderColumn BorderKind = iota + 1
	// BorderRow resizes adjacent rows within a column.
	BorderRow
)

// Border is an output-local hit zone between consecutive columns or rows.
type Border struct {
	// Kind selects the column or row edge.
	Kind BorderKind
	// Col and Row identify the adjacent cells.
	Col, Row int
	// Rect is the output-local pointer hit zone.
	Rect Rect
}

// borderMinWidth is the minimum logical pixel width for a border resize.
// The effective minimum also includes 5% of the lane and four border widths.
const borderMinWidth = 48

// columnBorders excludes fixed spiral slots, which have no shared resize edge.
func columnBorders(l layout.Layout, v layout.View) bool {
	return !l.Spiral() || l.LaneRow(v)
}

// BorderAt finds a resize hit zone at an output-local point.
func (w *Workspace) BorderAt(x, y, pad int) (Border, bool) {
	if w.Fullscreen != 0 || !contains(w.Usable, x, y) {
		return Border{}, false
	}
	// Overview and preview are Monitor presentation, not Workspace state. Their
	// caller must not ask for workspace borders while presenting those scenes.
	for _, p := range w.Layout() {
		if p.Floating && !p.Hidden && !p.Below && contains(p.Rect, x, y) {
			return Border{}, false
		}
	}
	rs := w.columnRects()
	l, v := w.Engine(), w.Snapshot()
	fixedFull := l.MaximizeHides() && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth
	borders := columnBorders(l, v)
	gap := w.Gap()
	// The lanes are walked once, not asked per column: lane is the one
	// holding column i, ending at laneLast.
	lane, laneLast := 0, -1
	for i, r := range rs {
		if i > laneLast {
			if i > 0 {
				lane++
			}
			_, laneLast = l.LaneBounds(v, lane)
		}
		if fixedFull && i != w.Focus {
			continue
		}
		if r.X+r.W <= w.Usable.X || r.X >= w.Usable.X+w.Usable.W || r.Y+r.H <= w.Usable.Y || r.Y >= w.Usable.Y+w.Usable.H {
			continue
		}
		rows := layout.RowRects(r, w.Columns[i], gap)
		for j := 0; j+1 < len(rows); j++ {
			lo, size := geom.BorderZone(rows[j].Y+rows[j].H, rows[j+1].Y, pad)
			b := Border{Kind: BorderRow, Col: i, Row: j, Rect: Rect{X: r.X, Y: lo, W: r.W, H: size}}
			if contains(b.Rect, x, y) {
				return b, true
			}
		}
		if fixedFull || !borders || i+1 >= len(rs) || i+1 > laneLast || w.Columns[i].FullWidth || w.Columns[i+1].FullWidth {
			continue
		}
		next := rs[i+1]
		if next.X >= w.Usable.X+w.Usable.W || next.X+next.W <= w.Usable.X {
			continue
		}
		top, bottom := max(r.Y, next.Y), min(r.Y+r.H, next.Y+next.H)
		lo, size := geom.BorderZone(r.X+r.W, next.X, pad)
		b := Border{Kind: BorderColumn, Col: i, Rect: Rect{X: lo, Y: top, W: size, H: max(bottom-top, 0)}}
		if contains(b.Rect, x, y) {
			return b, true
		}
	}
	return Border{}, false
}

// pixelShare rounds upward so a stamped width reproduces its integer pixels.
func pixelShare(pixels, total int) int {
	if total <= 0 {
		return 0
	}
	return (pixels*10000 + total - 1) / total
}

// MoveBorder moves border b to pos and reports whether any span or share
// changed (stamping a lane or a column's shares counts as a change).
func (w *Workspace) MoveBorder(b Border, pos int) bool {
	if b.Col < 0 || b.Col >= len(w.Columns) || w.Fullscreen != 0 {
		return false
	}
	rs := w.columnRects()
	r := rs[b.Col]
	if b.Kind == BorderRow {
		c := &w.Columns[b.Col]
		if b.Row < 0 || b.Row+1 >= len(c.Windows) || len(c.Windows)*minShare > 10000 {
			return false
		}
		rows := layout.RowRects(r, *c, w.Gap())
		avail := max(r.H-(len(rows)-1)*w.Gap(), 0)
		if avail == 0 {
			return false
		}
		stamped := !layout.ValidShares(*c)
		if stamped {
			c.Shares = make([]int, len(rows))
			used := 0
			for i := range rows {
				// Stamp the middle of the pixel bucket, leaving room for
				// pair-preserving rounding on subsequent resizes.
				c.Shares[i] = (rows[i].H*10000 + 5000) / avail
				used += c.Shares[i]
			}
			c.Shares[len(rows)-1] += 10000 - used
		}
		pair := c.Shares[b.Row] + c.Shares[b.Row+1]
		if pair < 2*minShare {
			return stamped
		}
		target := pixelShare(pos-w.Gap()/2-rows[b.Row].Y, avail)
		target = min(max(target, minShare), pair-minShare)
		if b.Row+1 < len(rows)-1 {
			target = pairPixelShare(target, pair, avail, rows[b.Row].H+rows[b.Row+1].H, minShare)
		}
		changed := stamped || c.Shares[b.Row] != target
		c.Shares[b.Row], c.Shares[b.Row+1] = target, pair-target
		return changed
	}
	if b.Kind != BorderColumn || b.Col+1 >= len(w.Columns) || w.LaneOf(b.Col) != w.LaneOf(b.Col+1) || !columnBorders(w.Engine(), w.Snapshot()) || w.Columns[b.Col].FullWidth || w.Columns[b.Col+1].FullWidth {
		return false
	}
	first, last := w.LaneBounds(w.LaneOf(b.Col))
	avail := max(w.Usable.W-2*w.Gap(), 0)
	if w.Engine().EqualCells() {
		avail = max(w.Usable.W-w.Gap()*(last-first+2), 0)
	}
	if avail == 0 {
		return false
	}
	stamped := !w.ValidSpans(first, last)
	if stamped {
		// Mutates spans, so it must not cache validity across this loop.
		stamp := w.NewStamp()
		used := 0
		for i := first; i <= last; i++ {
			w.Columns[i].Span = max((w.columnWidthFor(i, true)*10000+5000)/avail, 1)
			w.Columns[i].SpanStamp = stamp
			used += w.Columns[i].Span
		}
		if w.Engine().EqualCells() {
			w.Columns[last].Span += 10000 - used
		}
	}
	minimum := pixelShare(max(avail/20, 4*w.border, borderMinWidth), avail)
	target := pixelShare(pos-w.Gap()/2-r.X, avail)
	if !w.Engine().EqualCells() {
		span := min(max(target, min(minimum, 10000)), 10000)
		changed := stamped || w.Columns[b.Col].Span != span
		w.Columns[b.Col].Span = span
		return changed
	}
	pair := w.Columns[b.Col].Span + w.Columns[b.Col+1].Span
	if pair < 2*minimum {
		return stamped
	}
	target = min(max(target, minimum), pair-minimum)
	if b.Col+1 < last {
		target = pairPixelShare(target, pair, avail, rs[b.Col].W+rs[b.Col+1].W, minimum)
	}
	changed := stamped || w.Columns[b.Col].Span != target
	w.Columns[b.Col].Span, w.Columns[b.Col+1].Span = target, pair-target
	return changed
}

// ResetBorder restores automatic sizing at the given border.
func (w *Workspace) ResetBorder(b Border) {
	if b.Col < 0 || b.Col >= len(w.Columns) {
		return
	}
	switch b.Kind {
	case BorderColumn:
		w.ClearSpans(b.Col)
	case BorderRow:
		w.Columns[b.Col].Shares = nil
	}
}

// pairPixelShare keeps the pair's combined integer width unchanged as well as
// its basis-point sum. Independent floors can otherwise shift later tiles by
// one pixel even when the sum of the two shares is constant.
func pairPixelShare(target, pair, avail, pixels, minimum int) int {
	left := avail * target / 10000
	right := pixels - left
	lo := max(pixelShare(left, avail), pair-pixelShare(right+1, avail)+1, minimum)
	hi := min(pixelShare(left+1, avail)-1, pair-pixelShare(right, avail), pair-minimum)
	if lo <= hi {
		return min(max(target, lo), hi)
	}
	return target
}
