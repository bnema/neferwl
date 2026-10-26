package core

// Cascade sizing: lane sizing plus the band work around it. A full band
// makes room for a wider column by pushing a neighbor to the adjacent band,
// and the end of the width cycle (or unmaximizing) brings it back.

// Grow widens the selected column; cycle resets a fully grown lane.
func (cascadeLayout) Grow(w Edit, cycle bool) {
	w.ClearSpans(w.Focus)
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.Unmaximize()
	k := max(w.MaxColumns, 1)
	first, last := w.LaneBounds(w.LaneOf(w.Focus))
	n := last - first + 1
	pushed := false
	if cycle && n > 1 && n >= k && !w.unevenLane(first, last) {
		// A full band makes room: a neighbor goes to the adjacent band.
		w.pushNeighbor()
		first, last = w.LaneBounds(w.LaneOf(w.Focus))
		n, pushed = last-first+1, true
	}
	if n == 1 || n >= k {
		if cycle && !pushed && w.restoreBand() {
			// End of the cycle: the pushed columns came back.
			first, last = w.LaneBounds(w.LaneOf(w.Focus))
			w.setLaneCells(first, make([]int, last-first+1))
			return
		}
		cycleLane(w.Lanes, first, n, cycle)
		return
	}
	if !w.growLaneCell(first, last) && cycle {
		if w.restoreBand() {
			// End of the cycle: the pushed columns came back.
			first, last = w.LaneBounds(w.LaneOf(w.Focus))
			n = last - first + 1
		}
		w.setLaneCells(first, make([]int, n))
	}
}

// Shrink narrows the selected column.
func (cascadeLayout) Shrink(w Edit) {
	w.ClearSpans(w.Focus)
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.Unmaximize()
	shrinkLane(w.Lanes)
}

// ToggleFullWidth toggles maximization after workspace guards.
func (cascadeLayout) ToggleFullWidth(w Edit) {
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	} else {
		// A cascade column takes a band of its own; the others stay seen.
		w.isolateBand()
		w.Maximize(w.Focus)
	}
}

// unevenLane reports whether a column of the band first..last has its own
// width.
func (w *Lanes) unevenLane(first, last int) bool {
	for _, c := range w.Columns[first : last+1] {
		if c.Cells != 0 {
			return true
		}
	}
	return false
}

// unmaximizeBand unmaximizes the focused cascade column and brings back
// the columns its maximization sent to the adjacent bands.
func (w Edit) unmaximizeBand() {
	w.Columns[w.Focus].FullWidth = false
	w.restoreBand()
}
