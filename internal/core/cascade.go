package core

import "slices"

func (w *Workspace) band(i int) int         { return i / max(w.MaxColumns, 1) }
func (w *Workspace) sameBand(a, b int) bool { return !w.policy().wraps || w.band(a) == w.band(b) }

// cellX is the x of column i in its lane, from the usable area's left edge.
func (w *Workspace) cellX(i int) int {
	first, last := w.laneBounds(w.laneOf(i))
	S, u, sum := w.laneStats(first, last)
	x := w.gap()
	for j := first; j < i; j++ {
		x += w.laneWidth(j, first, last, S, u, sum) + w.gap()
	}
	return x
}
func (w *Workspace) cascadeBands() (n, spacing int) {
	if len(w.Columns) == 0 || w.Usable.H <= 0 {
		return 1, 0
	}
	return w.band(len(w.Columns)-1) + 1, w.Usable.H
}

// cellWidth is the width of column i in its lane: the columns of a band
// (the whole row in fixed overflow) share the usable width by their cells,
// auto columns sharing what the others leave, so a lone column fills it.
func (w *Workspace) cellWidth(i int) int {
	first, last := w.laneBounds(w.laneOf(i))
	S, u, sum := w.laneStats(first, last)
	return w.laneWidth(i, first, last, S, u, sum)
}

// laneStats measures the lane first..last: S is the sum of the cells of
// its set columns, u the count of its auto columns and sum the total of
// their weights (laneWeight).
func (w *Workspace) laneStats(first, last int) (S, u, sum int) {
	for i := first; i <= last; i++ {
		if c := w.Columns[i].Cells; c > 0 {
			S += c
		} else {
			u++
		}
	}
	for i := first; i <= last; i++ {
		sum += w.laneWeight(i, S, u)
	}
	return S, u, sum
}

// laneWeight is the share of column i in its lane, given its stats. With
// room left for every auto column, they share the cells the set ones leave
// and the set ones keep their cells; otherwise an auto column weighs one
// cell.
func (w *Workspace) laneWeight(i, S, u int) int {
	k := max(w.MaxColumns, 1)
	c := w.Columns[i].Cells
	switch {
	case u == 0:
		return c
	case k-S >= u:
		if c > 0 {
			return c * u
		}
		return k - S
	case c > 0:
		return c
	}
	return 1
}

// laneWidth is the width of column i of the lane first..last with the
// stats of laneStats: the usable width after gaps, split by weight. The
// remainder under one pixel per weight stays empty, as in equalShare.
func (w *Workspace) laneWidth(i, first, last, S, u, sum int) int {
	if sum <= 0 {
		return 0
	}
	avail := max(w.Usable.W-w.gap()*(last-first+2), 0)
	// floor(avail*weight/sum) without overflowing on a huge area.
	weight := w.laneWeight(i, S, u)
	return avail/sum*weight + avail%sum*weight/sum
}

// equalShare is the width of each of k columns sharing the usable width
// after gaps; a remainder under k pixels stays empty.
func (w *Workspace) equalShare(k int) int {
	k = max(k, 1)
	return max((w.Usable.W-w.gap()*(k+1))/k, 0)
}
func (w *Workspace) cellInBand(b int) int {
	k := max(w.MaxColumns, 1)
	return min(b*k+w.Focus%k, len(w.Columns)-1)
}
func (w *Workspace) focusBand(dir int) bool {
	i := w.laneNeighbor(dir)
	if i < 0 {
		return false
	}
	w.unmaximize()
	w.Focus = i
	w.Columns[i].Focus = 0
	if dir < 0 {
		w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
	}
	w.raiseColumns()
	w.scroll()
	return true
}
func (w *Workspace) cascadeRectsInto(dst []Rect, ignoreFullWidth bool) []Rect {
	g := w.gap()
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + w.shiftPixels()
	for lane := 0; ; lane++ {
		first, last := w.laneBounds(lane)
		if first > last {
			break
		}
		S, u, sum := w.laneStats(first, last)
		x := g
		for i := first; i <= last; i++ {
			cw := w.laneWidth(i, first, last, S, u, sum)
			r := Rect{X: w.Usable.X + x, Y: w.Usable.Y + g + w.band(i)*w.Usable.H - view, W: cw, H: max(w.Usable.H-2*g, 0)}
			if w.Columns[i].FullWidth && !ignoreFullWidth {
				r.X = w.Usable.X + g
				r.W = max(w.Usable.W-2*g, 0)
			}
			rects[i] = r
			x += cw + g
		}
	}
	return rects
}

func (w *Workspace) cascadePoints() []float64 {
	n, spacing := w.cascadeBands()
	points := make([]float64, n)
	for i := range points {
		points[i] = float64(i * spacing)
	}
	return points
}

func (w *Workspace) cascadeFocus(view int) int {
	if len(w.Columns) == 0 {
		return 0
	}
	b := min(max(view/max(w.Usable.H, 1), 0), w.band(len(w.Columns)-1))
	return w.cellInBand(b)
}

// laneOf is the lane of column i in the overview: its band, or the only
// lane of a layout that does not wrap.
func (w *Workspace) laneOf(i int) int {
	if w.policy().wraps {
		return w.band(i)
	}
	return 0
}

// laneBounds is the range of columns of a lane, first > last when the lane
// does not exist. A layout that does not wrap has the whole row as lane 0.
func (w *Workspace) laneBounds(lane int) (first, last int) {
	k := len(w.Columns)
	if w.policy().wraps {
		k = max(w.MaxColumns, 1)
	}
	first = lane * k
	if lane < 0 || first >= len(w.Columns) {
		return 0, -1
	}
	return first, min(first+k, len(w.Columns)) - 1
}

// leavesBand reports whether column i, where a left/right move from the
// focused column goes, is past the workspace or in another cascade band:
// the move then goes to the neighbor monitor first.
func (w *Workspace) leavesBand(i int) bool {
	return i < 0 || i >= len(w.Columns) || !w.sameBand(i, w.Focus)
}

// moveBand swaps the focused cascade column with the column in the band dir
// away where focus-window-up/down goes; focus follows it. Past the first
// or last band nothing moves.
func (w *Workspace) moveBand(dir int) {
	i := w.laneNeighbor(dir)
	if i < 0 {
		return
	}
	w.unmaximize()
	w.Columns[w.Focus], w.Columns[i] = w.Columns[i], w.Columns[w.Focus]
	w.Focus = i
	w.scroll()
}

// laneNeighbor is the closest column to the focus in the lane d lanes away,
// the focus's offset in its lane kept; -1 past the first or last lane.
func (w *Workspace) laneNeighbor(d int) int {
	lane := w.laneOf(w.Focus)
	first, _ := w.laneBounds(lane)
	nfirst, nlast := w.laneBounds(lane + d)
	if nfirst > nlast {
		return -1
	}
	return min(nfirst+w.Focus-first, nlast)
}
