package layout

func adjacentLane(l LaneSet, v View, d int) int {
	lane := l.LaneOf(v, v.Focus)
	first, _ := l.LaneBounds(v, lane)
	next, last := l.LaneBounds(v, lane+d)
	if next > last {
		return -1
	}
	return min(next+v.Focus-first, last)
}

// ScreenNeighbor is the column on that side of the focus on screen, -1 if none.
func ScreenNeighbor(v View, rects []Rect, dx, dy int) int {
	if v.Focus < 0 || v.Focus >= len(rects) {
		return -1
	}
	return GeometricNeighbor(rects, v.Focus, dx, dy)
}

// ScreenFocus reports whether directional focus follows the screen: a column
// is focused, and nothing fullscreen or maximized hides the others.
func ScreenFocus(v View) bool {
	return v.Focus >= 0 && v.Focus < len(v.Columns) && v.Fullscreen == 0 && !v.Columns[v.Focus].FullWidth
}

// WorkspaceStep maps a directional edge to the layout's workspace axis.
func WorkspaceStep(l Traits, axis Axis, dir int) int {
	_, workspace := l.Axes()
	if axis == workspace {
		return dir
	}
	return 0
}

// GeometricNeighbor selects the closest overlapping rect with stable tie breaks.
func GeometricNeighbor(rects []Rect, focus, dx, dy int) int {
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

// Snapshot snapshots layout inputs without retaining the Workspace.
func (w *Lanes) Snapshot() View {
	return View{Columns: w.Columns, Focus: w.Focus, View: w.View, MaxColumns: w.MaxColumns, Usable: w.Usable, Output: w.Output, Gap: w.Gap(), Fullscreen: w.Fullscreen, State: w.state(), Layout: w.Engine()}
}

// laneOf is the lane of column i in the overview: its band, or the only
// lane of a layout that does not wrap.
func (w *View) laneOf(i int) int {
	return w.Layout.LaneOf(*w, i)
}

// laneBounds is the range of columns of a lane, first > last when the lane
// does not exist. A layout that does not wrap has the whole row as lane 0.
func (w *View) laneBounds(lane int) (first, last int) {
	return w.Layout.LaneBounds(*w, lane)
}

// LeavesLane reports whether column i, where a left/right move from the
// focused column goes, is past the workspace or in another cascade band:
// the move then goes to the neighbor monitor first.
func (w *Lanes) LeavesLane(i int) bool {
	return i < 0 || i >= len(w.Columns) || w.LaneOf(i) != w.LaneOf(w.Focus)
}

// laneNeighbor is the closest column to the focus in the lane d lanes away,
// the focus's offset in its lane kept; -1 past the first or last lane.
func (w *View) laneNeighbor(d int) int {
	return w.Layout.LaneNeighbor(*w, d)
}
