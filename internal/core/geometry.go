package core

import "slices"

// geometryWorkspace adapts snapshot fields to the shared width and lane helpers.
// Only snapshot inputs are copied; scratch storage stays local to each call.
func geometryWorkspace(v layoutView) Workspace {
	return Workspace{lanes: lanes{Columns: v.Columns, Focus: v.Focus, View: v.View,
		MaxColumns: v.MaxColumns, Usable: v.Usable, Output: v.Output,
		Gaps: v.Gap, fullscreen: v.Fullscreen, lay: v.layout, lstate: v.State}}
}

func scrollSnapPoints(v layoutView, dst []float64) []float64 {
	if len(v.Columns) == 0 {
		return dst[:0]
	}
	w := geometryWorkspace(v)

	g := w.gap()
	minX, maxX := w.Usable.X+g, w.Usable.X+w.Usable.W-g
	last := len(w.Columns) - 1
	xs, widths := w.columnEdges()
	lo := xs[0] - minX
	hi := max(xs[last]+widths[last]-maxX, lo)
	views := make([]int, 0, 2*len(w.Columns))
	for i := range w.Columns {
		views = append(views, min(max(xs[i]-minX, lo), hi), min(max(xs[i]+widths[i]-maxX, lo), hi))
	}
	slices.Sort(views)
	minStep := float64(w.Usable.W) * snapSpacing
	if minStep <= 0 {
		points := append(dst[:0], float64(lo))
		for _, x := range views {
			if float64(x)-points[len(points)-1] >= minStep {
				points = append(points, float64(x))
			}
		}
		if float64(hi) != points[len(points)-1] {
			points = append(points, float64(hi))
		}
		return points
	}
	// Compact the sorted candidates in place before allocating the result.
	// This also keeps the gesture's destination reusable without growth steps.
	n := 1
	views[0] = lo
	for _, x := range views[1:] {
		if float64(x-views[n-1]) >= minStep {
			views[n] = x
			n++
		}
	}
	if hi != views[n-1] {
		if n > 1 && float64(hi-views[n-1]) < minStep {
			n--
		}
		views[n] = hi
		n++
	}
	points := slices.Grow(dst[:0], n)[:n]
	for i, x := range views[:n] {
		points[i] = float64(x)
	}
	return points
}

func scrollSnapFocus(v layoutView, view int, forward bool) (focus int) {
	if len(v.Columns) == 0 {
		return -1
	}
	w := geometryWorkspace(v)

	g := w.gap()
	minX, maxX := w.Usable.X+g, w.Usable.X+w.Usable.W-g
	xs, widths := w.columnEdges()
	full := func(i int) bool {
		x := xs[i] - view
		return x >= minX && x+widths[i] <= maxX
	}
	if full(w.Focus) && view == w.View {
		return w.Focus
	}
	focus = -1
	for i := range w.Columns {
		if full(i) && (focus < 0 || forward) {
			focus = i
		}
	}
	if focus < 0 {
		// Columns wider than the view: the one at the left edge.
		focus = 0
		for i := range w.Columns {
			if xs[i]-view <= minX {
				focus = i
			}
		}
	}
	return focus
}
