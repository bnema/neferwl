package core

import "slices"

func (w *Workspace) band(i int) int         { return i / max(w.MaxColumns, 1) }
func (w *Workspace) sameBand(a, b int) bool { return !w.policy().wraps || w.band(a) == w.band(b) }
func (w *Workspace) cellWidth() int {
	k := max(w.MaxColumns, 1)
	return max((w.Usable.W-w.gap()*(k+1))/k, 0)
}
func (w *Workspace) cellInBand(b int) int {
	k := max(w.MaxColumns, 1)
	return min(b*k+w.Focus%k, len(w.Columns)-1)
}
func (w *Workspace) focusBand(dir int) bool {
	i := w.bandNeighbor(dir)
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
func (w *Workspace) bandNeighbor(dir int) int {
	k := max(w.MaxColumns, 1)
	b := w.band(w.Focus) + dir
	if b < 0 || b*k >= len(w.Columns) {
		return -1
	}
	return w.cellInBand(b)
}

func (w *Workspace) cascadeRectsInto(dst []Rect, ignoreFullWidth bool) []Rect {
	k, g := max(w.MaxColumns, 1), w.gap()
	width := w.cellWidth()
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + w.shiftPixels()
	for i := range w.Columns {
		r := Rect{X: w.Usable.X + g + (i%k)*(width+g), Y: w.Usable.Y + g + w.band(i)*w.Usable.H - view, W: width, H: max(w.Usable.H-2*g, 0)}
		if w.Columns[i].FullWidth && !ignoreFullWidth {
			r.X = w.Usable.X + g
			r.W = max(w.Usable.W-2*g, 0)
		}
		rects[i] = r
	}
	return rects
}

func (w *Workspace) cascadePoints() []float64 {
	if len(w.Columns) == 0 || w.Usable.H <= 0 {
		return []float64{0}
	}
	points := make([]float64, w.band(len(w.Columns)-1)+1)
	for i := range points {
		points[i] = float64(i * w.Usable.H)
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
