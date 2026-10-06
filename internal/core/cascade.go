package core

import "slices"

func (w *Workspace) band(i int) int { return i / max(w.MaxColumns, 1) }
func (w *Workspace) bandNeighbor(dir int) int {
	k := max(w.MaxColumns, 1)
	b := w.band(w.Focus) + dir
	if b < 0 || b*k >= len(w.Columns) {
		return -1
	}
	return min(b*k+w.Focus%k, len(w.Columns)-1)
}

func (w *Workspace) cascadeRectsInto(dst []Rect, ignoreFullWidth bool) []Rect {
	k, g := max(w.MaxColumns, 1), w.gap()
	width := max((w.Usable.W-g*(k+1))/k, 0)
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
	k := max(w.MaxColumns, 1)
	b := min(max(view/max(w.Usable.H, 1), 0), w.band(len(w.Columns)-1))
	return min(b*k+w.Focus%k, len(w.Columns)-1)
}
