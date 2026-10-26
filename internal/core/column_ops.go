package core

import "slices"

func (w *Workspace) MoveColumn(dir int) {
	if w.onFloat() {
		return
	}
	if (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		if !w.layout().moveAllowed(w.lview(), w.Focus, w.Focus+dir) {
			return // a cascade column never crosses into the adjacent band
		}
		i := w.Focus
		w.Columns[i], w.Columns[i+dir] = w.Columns[i+dir], w.Columns[i]
		// Layout state keyed by position stays at its index.
		w.Focus += dir
		w.scroll()
	}
}

// MoveWindow moves the focused tile up or down (dir -1/1) inside its
// column; focus follows it. A slot window leaving row 0 releases the slot.
// Past the column, a cascade column moves to the adjacent band.
func (w *Workspace) MoveWindow(dir int) {
	if w.onFloat() || len(w.Columns) == 0 || (dir != -1 && dir != 1) || w.fullscreenHides() {
		return
	}
	c := &w.Columns[w.Focus]
	to := c.Focus + dir
	if to < 0 || to >= len(c.Windows) {
		if w.layout().moveLane(w.edit(), dir) {
			w.scroll()
		}
		return
	}
	if c.Focus == 0 || to == 0 {
		w.dropSlotOf(c.Windows[0])
	}
	c.Windows[c.Focus], c.Windows[to] = c.Windows[to], c.Windows[c.Focus]
	if validShares(*c) {
		c.Shares[c.Focus], c.Shares[to] = c.Shares[to], c.Shares[c.Focus]
	}
	c.Focus = to
	w.scroll()
}

// ResizeRow changes the focused window's height share by pct percent; the
// other rows of its column give or take it in proportion to their shares.
// Every row keeps at least minShare basis points.
func (w *Workspace) ResizeRow(pct int) {
	if w.onFloat() || len(w.Columns) == 0 || w.fullscreenHides() {
		return
	}
	c := &w.Columns[w.Focus]
	n := len(c.Windows)
	if n < 2 || n*minShare > 10000 {
		return
	}
	shares := c.Shares
	if !validShares(*c) {
		shares = make([]int, n)
		for i := range shares {
			shares[i] = 10000 / n
		}
		shares[n-1] += 10000 % n
	} else {
		shares = slices.Clone(shares)
	}
	f := c.Focus
	target := min(max(shares[f]+pct*100, minShare), 10000-minShare*(n-1))
	delta := target - shares[f]
	if delta == 0 {
		return
	}
	// The others share -delta in proportion to their room: what they have
	// above minShare when shrinking, their share when growing.
	weight := func(v int) int {
		if delta > 0 {
			return v - minShare
		}
		return v
	}
	total := 0
	for i, v := range shares {
		if i != f {
			total += weight(v)
		}
	}
	left := delta
	for i, v := range shares {
		if i == f {
			continue
		}
		part := 0
		if total > 0 {
			part = delta * weight(v) / total
		}
		shares[i] = v - part
		left -= part
	}
	shares[f] = target
	// Rounding: the remainder goes to the rows that still have room, last
	// first.
	for i := n - 1; left != 0 && i >= 0; i-- {
		if i == f {
			continue
		}
		step := left
		if left > 0 {
			step = min(left, shares[i]-minShare)
		}
		shares[i] -= step
		left -= step
	}
	c.Shares = shares
}

// GrowColumn widens the focused column by one cell, taking it from another
// column of its lane, or from the empty space in scroll overflow. A lane
// that cannot grow further goes back to equal shares when cycle is set
// (cycle-column-width); otherwise nothing changes (set-column-width +N).
// A lane is the band of a cascade, or the whole row in fixed overflow while
// it holds MaxColumns columns at most; past that the spiral ignores cells.
func (w *Workspace) GrowColumn(cycle bool) {
	if !w.sizable() {
		return
	}
	w.layout().grow(w.edit(), cycle)
	w.scroll()
}

// ShrinkColumn narrows the focused column by one cell.
func (w *Workspace) ShrinkColumn() {
	if !w.sizable() {
		return
	}
	w.layout().shrink(w.edit())
	w.scroll()
}

// scrollCells is the width in cells of column i in scroll overflow: its
// cells, at most the ones in view for an auto column.
func (w *lanes) scrollCells(i int) int {
	if c := w.Columns[i].Cells; c > 0 {
		return c
	}
	k := max(w.MaxColumns, 1)
	return k / min(len(w.Columns), k)
}

// laneCells is the cells of the lane first..last, summing to MaxColumns.
// The stored cells are used when every column is set and they sum to it
// (ok); otherwise each column gets an equal share and the remainder goes
// to column remAt, or one cell each to the leftmost columns when remAt is
// negative.
func (w *lanes) laneCells(first, last, remAt int) (cells []int, ok bool) {
	k := max(w.MaxColumns, 1)
	n := last - first + 1
	cells = make([]int, n)
	sum := 0
	for i := range cells {
		cells[i] = w.Columns[first+i].Cells
		sum += cells[i]
		if cells[i] == 0 {
			sum = -1 << 30
		}
	}
	if sum == k {
		return cells, true
	}
	for i := range cells {
		cells[i] = k / n
	}
	if remAt >= 0 {
		cells[remAt-first] += k % n
	} else {
		for i := range k % n {
			cells[i]++
		}
	}
	return cells, false
}

// setLaneCells stores cells on the lane's columns, from column first.
func (w *lanes) setLaneCells(first int, cells []int) {
	for i, c := range cells {
		w.Columns[first+i].Cells = c
	}
}

// donor is the lane column that gives a cell to the focused one: among the
// others with more than one cell, the largest; on a tie the nearest to the
// focus, the right side first. -1 when there is none.
func (w *lanes) donor(first, last int, cells []int) int {
	best := -1
	for i := first; i <= last; i++ {
		c := cells[i-first]
		if i == w.Focus || c <= 1 {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		bc, bd, d := cells[best-first], abs(best-w.Focus), abs(i-w.Focus)
		if c > bc || c == bc && (d < bd || d == bd && i > w.Focus) {
			best = i
		}
	}
	return best
}

// moveCell moves one cell from column from to column to of the lane that
// starts at first, and stores the lane.
func (w *lanes) moveCell(first int, cells []int, from, to int) {
	cells[from-first]--
	cells[to-first]++
	w.setLaneCells(first, cells)
}
