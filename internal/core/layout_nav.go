package core

// laneWorkspace exposes only the snapshot fields needed by the band walk.
func laneWorkspace(v layoutView) Workspace {
	return Workspace{Columns: v.Columns, Focus: v.Focus, MaxColumns: v.MaxColumns, lay: v.layout, lstate: v.State}
}

func adjacentLane(l layoutLanes, v layoutView, d int) int {
	lane := l.laneOf(v, v.Focus)
	first, _ := l.laneBounds(v, lane)
	next, last := l.laneBounds(v, lane+d)
	if next > last {
		return -1
	}
	return min(next+v.Focus-first, last)
}

// screenNeighbor is the column on that side of the focus on screen, -1 if none.
func screenNeighbor(v layoutView, rects []Rect, dx, dy int) int {
	if v.Focus < 0 || v.Focus >= len(rects) {
		return -1
	}
	return geometricNeighbor(rects, v.Focus, dx, dy)
}

// screenFocus reports whether directional focus follows the screen: a column
// is focused, and nothing fullscreen or maximized hides the others.
func screenFocus(v layoutView) bool {
	return v.Focus >= 0 && v.Focus < len(v.Columns) && v.Fullscreen == 0 && !v.Columns[v.Focus].FullWidth
}

// workspaceStep maps a directional edge to the layout's workspace axis.
func workspaceStep(l layoutTraits, axis layoutAxis, dir int) int {
	_, workspace := l.axes()
	if axis == workspace {
		return dir
	}
	return 0
}

// geometricNeighbor selects the closest overlapping rect with stable tie breaks.
func geometricNeighbor(rects []Rect, focus, dx, dy int) int {
	if focus < 0 || focus >= len(rects) {
		return -1
	}
	cur := rects[focus]
	best, bestDist, bestOverlap := -1, 0, 0
	for i, r := range rects {
		var overlap, dist int
		switch {
		case dx < 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), cur.X-(r.X+r.W)
		case dx > 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), r.X-(cur.X+cur.W)
		case dy < 0:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), cur.Y-(r.Y+r.H)
		default:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), r.Y-(cur.Y+cur.H)
		}
		if i == focus || overlap <= 0 || dist < 0 {
			continue
		}
		closer := best >= 0 && (dist < bestDist || dist == bestDist && (overlap > bestOverlap ||
			overlap == bestOverlap && abs(i-focus) < abs(best-focus)))
		if best < 0 || closer {
			best, bestDist, bestOverlap = i, dist, overlap
		}
	}
	return best
}

// lview snapshots layout inputs without retaining the Workspace.
func (w *Workspace) lview() layoutView {
	return layoutView{Columns: w.Columns, Focus: w.Focus, View: w.View, MaxColumns: w.MaxColumns, Usable: w.Usable, Output: w.Output, Gap: w.gap(), Fullscreen: w.fullscreen, State: w.state(), layout: w.layout()}
}

// focusLane focuses the column in the lane dir away; a maximized column
// keeps its own band, so it stays maximized.
func (w *Workspace) focusLane(dir int) bool {
	i := w.laneNeighbor(dir)
	if i < 0 {
		return false
	}
	w.Focus = i
	w.Columns[i].Focus = 0
	if dir < 0 {
		w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
	}
	w.raiseColumns()
	w.scroll()
	return true
}

// laneOf is the lane of column i in the overview: its band, or the only
// lane of a layout that does not wrap.
func (w *Workspace) laneOf(i int) int {
	return w.layout().laneOf(w.lview(), i)
}

// laneBounds is the range of columns of a lane, first > last when the lane
// does not exist. A layout that does not wrap has the whole row as lane 0.
func (w *Workspace) laneBounds(lane int) (first, last int) {
	return w.layout().laneBounds(w.lview(), lane)
}

// leavesLane reports whether column i, where a left/right move from the
// focused column goes, is past the workspace or in another cascade band:
// the move then goes to the neighbor monitor first.
func (w *Workspace) leavesLane(i int) bool {
	return i < 0 || i >= len(w.Columns) || w.laneOf(i) != w.laneOf(w.Focus)
}

// laneNeighbor is the closest column to the focus in the lane d lanes away,
// the focus's offset in its lane kept; -1 past the first or last lane.
func (w *Workspace) laneNeighbor(d int) int {
	return w.layout().laneNeighbor(w.lview(), d)
}
