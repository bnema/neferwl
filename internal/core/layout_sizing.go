package core

import "slices"

// Sizers compose the pieces below: every layout starts with sizable and the
// clearing of manual widths, then sizes by cells (growCells, shrinkCells), by
// equal lane shares (growLane, shrinkLane), or both with its own lane work
// around them (cascade_sizing.go).

func (columnsBase) grow(w edit, cycle bool) { growCells(w.lanes, cycle) }
func (columnsBase) shrink(w edit)           { shrinkCells(w.lanes) }
func (columnsBase) toggleFullWidth(w edit)  { toggleMaximized(w.lanes) }

func (fixedLayout) grow(w edit, cycle bool) {
	w.clearSpans(w.Focus)
	w.unmaximize()
	growLane(w.lanes, cycle)
}

func (fixedLayout) shrink(w edit) {
	w.clearSpans(w.Focus)
	w.unmaximize()
	shrinkLane(w.lanes)
}

// growCells widens the focused column by a cell of the row, up to MaxColumns;
// cycle then goes back to auto. The rest of the row keeps its width.
func growCells(w *lanes, cycle bool) {
	w.clearSpans(w.Focus)
	w.unmaximize()
	k := max(w.MaxColumns, 1)
	c := &w.Columns[w.Focus]
	cur := w.scrollCells(w.Focus)
	switch {
	case cur < k:
		c.Cells = cur + 1
	case cycle:
		c.Cells = 0
	}
}

// shrinkCells narrows the focused column by a cell, down to one.
func shrinkCells(w *lanes) {
	w.clearSpans(w.Focus)
	w.unmaximize()
	w.Columns[w.Focus].Cells = max(w.scrollCells(w.Focus)-1, 1)
}

// growLane moves a cell from another column of the focused lane to the
// focused column; cycle returns an uneven lane to equal cells at the end.
// Only lanes sized by cells in one row grow.
func growLane(w *lanes, cycle bool) {
	if !w.laneRow() {
		return
	}
	first, last := w.laneBounds(w.laneOf(w.Focus))
	n := last - first + 1
	if n == 1 || n >= max(w.MaxColumns, 1) {
		cycleLane(w, first, n, cycle)
		return
	}
	if !w.growLaneCell(first, last) && cycle {
		w.setLaneCells(first, make([]int, n))
	}
}

// cycleLane returns a lane that cannot grow, but was left uneven (a column
// joined it, or max-columns shrank), to equal cells when cycling.
func cycleLane(w *lanes, first, n int, cycle bool) {
	if cycle && slices.ContainsFunc(w.Columns[first:first+n], func(c Column) bool { return c.Cells != 0 }) {
		w.setLaneCells(first, make([]int, n))
	}
}

// growLaneCell gives the focused column a cell of the lane first..last,
// taken from the best donor. It reports false when no column has a cell to
// spare.
func (w *lanes) growLaneCell(first, last int) bool {
	k, n := max(w.MaxColumns, 1), last-first+1
	cells, ok := w.laneCells(first, last, w.Focus)
	if !ok {
		w.setLaneCells(first, cells)
		if k%n > 0 {
			return true // The remainder to the focused column was the step.
		}
	}
	d := w.donor(first, last, cells)
	if d < 0 {
		return false
	}
	w.moveCell(first, cells, d, w.Focus)
	return true
}

// shrinkLane narrows the focused column by one cell, giving it to its right
// neighbor in the lane (the left one at the lane's end). A column never goes
// below one cell.
func shrinkLane(w *lanes) {
	if !w.laneRow() {
		return
	}
	first, last := w.laneBounds(w.laneOf(w.Focus))
	n := last - first + 1
	if n == 1 || n >= max(w.MaxColumns, 1) {
		return
	}
	cells, _ := w.laneCells(first, last, -1)
	if cells[w.Focus-first] <= 1 {
		return
	}
	to := w.Focus + 1
	if to > last {
		to = w.Focus - 1
	}
	w.moveCell(first, cells, w.Focus, to)
}

// toggleMaximized expands the focused tiled column without changing its saved
// width, or restores it.
func toggleMaximized(w *lanes) {
	if w.Columns[w.Focus].FullWidth {
		w.unmaximize()
	} else {
		w.maximize(w.Focus)
	}
}
