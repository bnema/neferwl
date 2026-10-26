package layout

// scrollCells is the width in cells of column i in scroll overflow: its
// cells, at most the ones in view for an auto column.
func (w *Lanes) scrollCells(i int) int {
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
func (w *Lanes) laneCells(first, last, remAt int) (cells []int, ok bool) {
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
func (w *Lanes) setLaneCells(first int, cells []int) {
	for i, c := range cells {
		w.Columns[first+i].Cells = c
	}
}

// donor is the lane column that gives a cell to the focused one: among the
// others with more than one cell, the largest; on a tie the nearest to the
// focus, the right side first. -1 when there is none.
func (w *Lanes) donor(first, last int, cells []int) int {
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
func (w *Lanes) moveCell(first int, cells []int, from, to int) {
	cells[from-first]--
	cells[to-first]++
	w.setLaneCells(first, cells)
}
