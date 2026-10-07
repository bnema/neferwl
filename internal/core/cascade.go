package core

import "slices"

func (w *Workspace) band(i int) int         { return i / max(w.MaxColumns, 1) }
func (w *Workspace) sameBand(a, b int) bool { return !w.policy().wraps || w.band(a) == w.band(b) }
func (w *Workspace) cellX(i int) int {
	return w.gap() + (i%max(w.MaxColumns, 1))*(w.cellWidth(i)+w.gap())
}
func (w *Workspace) cascadeBands() (n, spacing int) {
	if len(w.Columns) == 0 || w.Usable.H <= 0 {
		return 1, 0
	}
	return w.band(len(w.Columns)-1) + 1, w.Usable.H
}
// cellWidth is the width of the cell of column i: the columns of its band
// share the usable width equally, as in fixed overflow, so a lone column
// fills it.
func (w *Workspace) cellWidth(i int) int {
	first, last := w.laneBounds(w.band(i))
	k := max(last-first+1, 1)
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
	for i := range w.Columns {
		r := Rect{X: w.Usable.X + w.cellX(i), Y: w.Usable.Y + g + w.band(i)*w.Usable.H - view, W: w.cellWidth(i), H: max(w.Usable.H-2*g, 0)}
		if w.Columns[i].FullWidth && !ignoreFullWidth {
			r.X = w.Usable.X + g
			r.W = max(w.Usable.W-2*g, 0)
		}
		rects[i] = r
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
