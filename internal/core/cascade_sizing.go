package core

// Cascade sizing: lane sizing plus the band work around it. A full band
// makes room for a wider column by pushing a neighbor to the adjacent band,
// and the end of the width cycle (or unmaximizing) brings it back.

func (cascadeLayout) grow(w edit, cycle bool) {
	w.clearSpans(w.Focus)
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.unmaximize()
	k := max(w.MaxColumns, 1)
	first, last := w.laneBounds(w.laneOf(w.Focus))
	n := last - first + 1
	pushed := false
	if cycle && n > 1 && n >= k && !w.unevenLane(first, last) {
		// A full band makes room: a neighbor goes to the adjacent band.
		w.pushNeighbor()
		first, last = w.laneBounds(w.laneOf(w.Focus))
		n, pushed = last-first+1, true
	}
	if n == 1 || n >= k {
		if cycle && !pushed && w.restoreBand() {
			// End of the cycle: the pushed columns came back.
			first, last = w.laneBounds(w.laneOf(w.Focus))
			w.setLaneCells(first, make([]int, last-first+1))
			return
		}
		cycleLane(w.lanes, first, n, cycle)
		return
	}
	if !w.growLaneCell(first, last) && cycle {
		if w.restoreBand() {
			// End of the cycle: the pushed columns came back.
			first, last = w.laneBounds(w.laneOf(w.Focus))
			n = last - first + 1
		}
		w.setLaneCells(first, make([]int, n))
	}
}

func (cascadeLayout) shrink(w edit) {
	w.clearSpans(w.Focus)
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.unmaximize()
	shrinkLane(w.lanes)
}

func (cascadeLayout) toggleFullWidth(w edit) {
	if w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	} else {
		// A cascade column takes a band of its own; the others stay seen.
		w.isolateBand()
		w.maximize(w.Focus)
	}
}

// unevenLane reports whether a column of the band first..last has its own
// width.
func (w *lanes) unevenLane(first, last int) bool {
	for _, c := range w.Columns[first : last+1] {
		if c.Cells != 0 {
			return true
		}
	}
	return false
}

// unmaximizeBand unmaximizes the focused cascade column and brings back
// the columns its maximization sent to the adjacent bands.
func (w edit) unmaximizeBand() {
	w.Columns[w.Focus].FullWidth = false
	w.restoreBand()
}
