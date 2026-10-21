package core

type borderKind int

const (
	borderColumn borderKind = iota + 1
	borderRow
)

// border is an output-local hit zone between consecutive columns or rows.
type border struct {
	kind     borderKind
	col, row int
	rect     Rect
}

// borderMinWidth is the minimum logical pixel width for a border resize.
// The effective minimum also includes 5% of the lane and four border widths.
const borderMinWidth = 48

func borderZone(a, b, pad int) (int, int) {
	mid := (a + b) / 2
	lo, hi := min(a, mid-pad), max(b, mid+pad)
	return lo, max(hi-lo, 1)
}

func (w *Workspace) borderAt(x, y, pad int) (border, bool) {
	if w.fullscreen != 0 || !contains(w.Usable, x, y) {
		return border{}, false
	}
	// Overview and preview are Monitor presentation, not Workspace state. Their
	// caller must not ask for workspace borders while presenting those scenes.
	for _, p := range w.Layout() {
		if p.Floating && !p.Hidden && !p.Below && contains(p.Rect, x, y) {
			return border{}, false
		}
	}
	rs := w.columnRects()
	fixedFull := w.Overflow == OverflowFixed && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth
	for i, r := range rs {
		if fixedFull && i != w.Focus {
			continue
		}
		if r.X+r.W <= w.Usable.X || r.X >= w.Usable.X+w.Usable.W || r.Y+r.H <= w.Usable.Y || r.Y >= w.Usable.Y+w.Usable.H {
			continue
		}
		rows := rowRects(r, w.Columns[i], w.gap())
		for j := 0; j+1 < len(rows); j++ {
			lo, size := borderZone(rows[j].Y+rows[j].H, rows[j+1].Y, pad)
			b := border{kind: borderRow, col: i, row: j, rect: Rect{X: r.X, Y: lo, W: r.W, H: size}}
			if contains(b.rect, x, y) {
				return b, true
			}
		}
		if fixedFull || w.Overflow == OverflowFixed && !w.laneRow() || i+1 >= len(rs) || w.laneOf(i) != w.laneOf(i+1) || w.Columns[i].FullWidth || w.Columns[i+1].FullWidth {
			continue
		}
		next := rs[i+1]
		if next.X >= w.Usable.X+w.Usable.W || next.X+next.W <= w.Usable.X {
			continue
		}
		top, bottom := max(r.Y, next.Y), min(r.Y+r.H, next.Y+next.H)
		lo, size := borderZone(r.X+r.W, next.X, pad)
		b := border{kind: borderColumn, col: i, rect: Rect{X: lo, Y: top, W: size, H: max(bottom-top, 0)}}
		if contains(b.rect, x, y) {
			return b, true
		}
	}
	return border{}, false
}

// pixelShare rounds upward so a stamped width reproduces its integer pixels.
func pixelShare(pixels, total int) int {
	if total <= 0 {
		return 0
	}
	return (pixels*10000 + total - 1) / total
}

// setBorder moves border b to pos and reports whether any span or share
// changed (stamping a lane or a column's shares counts as a change).
func (w *Workspace) setBorder(b border, pos int) bool {
	if b.col < 0 || b.col >= len(w.Columns) || w.fullscreen != 0 {
		return false
	}
	rs := w.columnRects()
	r := rs[b.col]
	if b.kind == borderRow {
		c := &w.Columns[b.col]
		if b.row < 0 || b.row+1 >= len(c.Windows) || len(c.Windows)*minShare > 10000 {
			return false
		}
		rows := rowRects(r, *c, w.gap())
		avail := max(r.H-(len(rows)-1)*w.gap(), 0)
		if avail == 0 {
			return false
		}
		stamped := !validShares(*c)
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
		pair := c.Shares[b.row] + c.Shares[b.row+1]
		if pair < 2*minShare {
			return stamped
		}
		target := pixelShare(pos-w.gap()/2-rows[b.row].Y, avail)
		target = min(max(target, minShare), pair-minShare)
		if b.row+1 < len(rows)-1 {
			target = pairPixelShare(target, pair, avail, rows[b.row].H+rows[b.row+1].H, minShare)
		}
		changed := stamped || c.Shares[b.row] != target
		c.Shares[b.row], c.Shares[b.row+1] = target, pair-target
		return changed
	}
	if b.kind != borderColumn || b.col+1 >= len(w.Columns) || w.laneOf(b.col) != w.laneOf(b.col+1) || w.Overflow == OverflowFixed && !w.laneRow() || w.Columns[b.col].FullWidth || w.Columns[b.col+1].FullWidth {
		return false
	}
	first, last := w.laneBounds(w.laneOf(b.col))
	avail := max(w.Usable.W-2*w.gap(), 0)
	if w.policy().equalCells {
		avail = max(w.Usable.W-w.gap()*(last-first+2), 0)
	}
	if avail == 0 {
		return false
	}
	stamped := !w.validSpans(first, last)
	if stamped {
		w.spanSeq++
		used := 0
		for i := first; i <= last; i++ {
			w.Columns[i].Span = max((w.columnWidthFor(i, true)*10000+5000)/avail, 1)
			w.Columns[i].spanStamp = w.spanSeq
			used += w.Columns[i].Span
		}
		if w.policy().equalCells {
			w.Columns[last].Span += 10000 - used
		}
	}
	minimum := pixelShare(max(avail/20, 4*w.border, borderMinWidth), avail)
	target := pixelShare(pos-w.gap()/2-r.X, avail)
	if !w.policy().equalCells {
		span := min(max(target, min(minimum, 10000)), 10000)
		changed := stamped || w.Columns[b.col].Span != span
		w.Columns[b.col].Span = span
		return changed
	}
	pair := w.Columns[b.col].Span + w.Columns[b.col+1].Span
	if pair < 2*minimum {
		return stamped
	}
	target = min(max(target, minimum), pair-minimum)
	if b.col+1 < last {
		target = pairPixelShare(target, pair, avail, rs[b.col].W+rs[b.col+1].W, minimum)
	}
	changed := stamped || w.Columns[b.col].Span != target
	w.Columns[b.col].Span, w.Columns[b.col+1].Span = target, pair-target
	return changed
}

func (w *Workspace) resetBorder(b border) {
	if b.col < 0 || b.col >= len(w.Columns) {
		return
	}
	switch b.kind {
	case borderColumn:
		w.clearSpans(b.col)
	case borderRow:
		w.Columns[b.col].Shares = nil
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
