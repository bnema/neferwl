package layout

import "slices"

// Gap clamps requested spacing to the usable viewport.
func (w *Lanes) Gap() int { return min(w.Gaps, w.Usable.W/2, w.Usable.H/2) }

// StackRects cuts r in n rows with a gap between them; the last row takes
// the rounding remainder. Rows past the bottom (gaps taller than r) are
// clamped to it, like windows stacked in a column.
func StackRects(r Rect, n, gap int) []Rect {
	return StackRectsInto(nil, r, n, gap)
}

// StackRectsInto appends the n rows of stackRects to dst (reset with dst[:0]
// to reuse its storage).
func StackRectsInto(dst []Rect, r Rect, n, gap int) []Rect {
	base := len(dst)
	dst = slices.Grow(dst, n)[:base+n]
	rows := dst[base:]
	avail := max(r.H-(n-1)*gap, 0)
	height := avail / n
	yy, bottom := r.Y, r.Y+r.H
	for i := range rows {
		hh := height
		if i == n-1 {
			hh = avail - height*(n-1)
		}
		yy = min(yy, bottom)
		hh = min(hh, bottom-yy)
		rows[i] = Rect{X: r.X, Y: yy, W: r.W, H: hh}
		yy += hh + gap
	}
	return dst
}

// RowRects splits column rect r into the rows of c: by c.Shares when they
// are valid, else equally (stackRects).
func RowRects(r Rect, c Column, gap int) []Rect {
	return RowRectsInto(nil, r, c, gap)
}

// RowRectsInto appends the rows of rowRects to dst (reset with dst[:0] to
// reuse its storage).
func RowRectsInto(dst []Rect, r Rect, c Column, gap int) []Rect {
	n := len(c.Windows)
	if !ValidShares(c) {
		return StackRectsInto(dst, r, n, gap)
	}
	base := len(dst)
	dst = slices.Grow(dst, n)[:base+n]
	rows := dst[base:]
	avail := max(r.H-(n-1)*gap, 0)
	yy, bottom, used := r.Y, r.Y+r.H, 0
	for i, share := range c.Shares {
		hh := avail * share / 10000
		if i == n-1 {
			hh = avail - used
		}
		used += hh
		yy = min(yy, bottom)
		hh = min(hh, bottom-yy)
		rows[i] = Rect{X: r.X, Y: yy, W: r.W, H: hh}
		yy += hh + gap
	}
	return dst
}

// ValidShares reports whether c.Shares has one share per window, each at
// least 1, summing to 10000.
func ValidShares(c Column) bool {
	if len(c.Shares) != len(c.Windows) || len(c.Shares) < 2 {
		return false
	}
	sum := 0
	for _, v := range c.Shares {
		if v < 1 {
			return false
		}
		sum += v
	}
	return sum == 10000
}

// Split cuts r in two halves with a gap: top/bottom when vertical, else left/right.
func Split(r Rect, gap int, vertical bool) (Rect, Rect) {
	if vertical {
		h := max((r.H-gap)/2, 0)
		return Rect{X: r.X, Y: r.Y, W: r.W, H: h}, Rect{X: r.X, Y: r.Y + h + gap, W: r.W, H: max(r.H-gap-h, 0)}
	}
	w := max((r.W-gap)/2, 0)
	return Rect{X: r.X, Y: r.Y, W: w, H: r.H}, Rect{X: r.X + w + gap, Y: r.Y, W: max(r.W-gap-w, 0), H: r.H}
}
