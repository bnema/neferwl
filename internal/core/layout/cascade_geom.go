package layout

func (w *View) cascadeBands() (n, spacing int) {
	if len(w.Columns) == 0 || w.Usable.H <= 0 {
		return 1, 0
	}
	return w.band(len(w.Columns)-1) + 1, w.Usable.H
}

// cellWidth is the width of column i in its lane: the columns of a band
// (the whole row in fixed overflow) share the usable width by their cells,
// auto columns sharing what the others leave, so a lone column fills it.
func (w *View) cellWidth(i int) int {
	first, last := w.laneBounds(w.laneOf(i))
	S, u, sum := w.laneStats(first, last)
	return w.laneWidth(i, first, last, S, u, sum, w.validSpans(first, last))
}

// laneStats measures the lane first..last: S is the sum of the cells of
// its set columns, u the count of its auto columns and sum the total of
// their weights (laneWeight).
func (w *View) laneStats(first, last int) (S, u, sum int) {
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
func (w *View) laneWeight(i, S, u int) int {
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
// last column receives the pixel remainder, just as for stamped spans.
// The caller passes the lane's validSpans so a loop over the lane checks it once.
func (w *View) laneWidth(i, first, last, S, u, sum int, spansValid bool) int {
	if spansValid {
		avail := max(w.Usable.W-w.gap()*(last-first+2), 0)
		if i == last {
			used := 0
			for j := first; j < last; j++ {
				used += avail * w.Columns[j].Span / 10000
			}
			return avail - used
		}
		return avail * w.Columns[i].Span / 10000
	}
	if sum <= 0 {
		return 0
	}
	avail := max(w.Usable.W-w.gap()*(last-first+2), 0)
	// floor(avail*weight/sum) without overflowing on a huge area.
	width := func(j int) int {
		weight := w.laneWeight(j, S, u)
		return avail/sum*weight + avail%sum*weight/sum
	}
	if i == last {
		used := 0
		for j := first; j < last; j++ {
			used += width(j)
		}
		return avail - used
	}
	return width(i)
}

// equalShare is the width of each of k columns sharing the usable width
// after gaps; a remainder under k pixels stays empty.
func (w *View) equalShare(k int) int {
	k = max(k, 1)
	return max((w.Usable.W-w.gap()*(k+1))/k, 0)
}
