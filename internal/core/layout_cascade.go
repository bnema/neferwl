package core

import "slices"

// OverflowCascade wraps columns into vertically scrolling viewport bands.
const OverflowCascade Overflow = "cascade"

// cascadeLayout wraps columns into vertically scrolling viewport bands.
type cascadeLayout struct{ columnsBase }

func (cascadeLayout) name() Overflow          { return OverflowCascade }
func (cascadeLayout) equalCells() bool        { return true }
func (cascadeLayout) pinsFullscreen() bool    { return true }
func (cascadeLayout) resetsMaximize() bool    { return true }
func (cascadeLayout) bands() bool             { return true }
func (cascadeLayout) laneGestures() bool      { return true }
func (cascadeLayout) laneRow(layoutView) bool { return true }
func (cascadeLayout) columnRects(v layoutView, dst []Rect, o rectOpts) []Rect {
	w := geometryWorkspace(v)
	ignoreFullWidth, shift := o.IgnoreFullWidth, o.Shift

	g := w.gap()
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + shift
	w.walkBands(func(lane, first, last int) bool {
		S, u, sum := w.laneStats(first, last)
		x := g
		valid := w.validSpans(first, last)
		for i := first; i <= last; i++ {
			cw := w.laneWidth(i, first, last, S, u, sum, valid)
			r := Rect{X: w.Usable.X + x, Y: w.Usable.Y + g + lane*w.Usable.H - view, W: cw, H: max(w.Usable.H-2*g, 0)}
			if w.Columns[i].FullWidth && !ignoreFullWidth {
				r.X = w.Usable.X + g
				r.W = max(w.Usable.W-2*g, 0)
			}
			rects[i] = r
			x += cw + g
		}
		return true
	})
	return rects
}
func (cascadeLayout) snapPoints(v layoutView, dst []float64) []float64 {
	w := geometryWorkspace(v)

	n, spacing := w.cascadeBands()
	points := slices.Grow(dst[:0], n)[:n]
	for i := range points {
		points[i] = float64(i * spacing)
	}
	return points
}
func (cascadeLayout) snapFocus(v layoutView, view int, forward bool) int {
	w := geometryWorkspace(v)

	if len(w.Columns) == 0 {
		return -1
	}
	b := min(max(view/max(w.Usable.H, 1), 0), w.band(len(w.Columns)-1))
	return w.laneNeighbor(b - w.band(w.Focus))
}
func (cascadeLayout) laneOf(v layoutView, i int) int {
	w := laneWorkspace(v)
	return w.band(i)
}
func (cascadeLayout) laneBounds(v layoutView, lane int) (int, int) {
	w := laneWorkspace(v)
	return w.bandBounds(lane)
}
func (cascadeLayout) laneNeighbor(v layoutView, d int) int {
	return adjacentLane(v.layout, v, d)
}
func (cascadeLayout) scrollTarget(v layoutView) int {
	w := geometryWorkspace(v)
	return w.band(v.Focus) * v.Usable.H
}
func (l cascadeLayout) focusStep(v layoutView, dir int) int {
	i := l.columnsBase.focusStep(v, dir)
	if i < 0 || v.layout.laneOf(v, i) != v.layout.laneOf(v, v.Focus) {
		return -1
	}
	return i
}
func (cascadeLayout) coveredSpawnAt(v layoutView) int { return len(v.Columns) }
func (cascadeLayout) previewTiles(v layoutView, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	w := geometryWorkspace(v)
	var sel Rect
	// One walk over the bands: each lane is measured once and its columns
	// step through it left to right.
	w.walkBands(func(lane, first, last int) bool {
		S, u, sum := w.laneStats(first, last)
		valid := w.validSpans(first, last)
		x := v.Gap
		for i := first; i <= last; i++ {
			c := v.Columns[i]
			cw := w.laneWidth(i, first, last, S, u, sum, valid)
			r := Rect{X: x, Y: v.Gap + lane*v.Usable.H, W: cw, H: max(v.Usable.H-2*v.Gap, 0)}
			x += cw + v.Gap
			dst = appendPreviewColumn(dst, rowBuf, r, c, v.Gap, i == v.Focus)
			if i == v.Focus {
				sel = r
			}
		}
		return true
	})
	span := 0
	if len(v.Columns) > 0 {
		span = v.Usable.W
	}
	return dst, span, sel
}
func (cascadeLayout) axes() (layoutAxis, layoutAxis) { return verticalAxis, horizontalAxis }
func (cascadeLayout) laneCount(v layoutView) int {
	if len(v.Columns) == 0 {
		return 0
	}
	w := laneWorkspace(v)
	return w.band(len(v.Columns)-1) + 1
}
func (cascadeLayout) laneStep(v layoutView, dir int) int { return v.layout.laneNeighbor(v, dir) }
func (cascadeLayout) spawnAt(v layoutView) (int, insertKind) {
	if len(v.Columns) == 0 {
		return 0, insertNewLane
	}
	first, last := v.layout.laneBounds(v, v.layout.laneOf(v, v.Focus))
	full := last-first+1 >= max(v.MaxColumns, 1)
	for _, c := range v.Columns[first : last+1] {
		full = full || c.FullWidth
	}
	if full {
		return last + 1, insertNewLane
	}
	return last + 1, insertDefault
}
func (cascadeLayout) moveAllowed(v layoutView, from, to int) bool {
	if from < 0 || to < 0 || from >= len(v.Columns) || to >= len(v.Columns) {
		return false
	}
	return v.layout.laneOf(v, from) == v.layout.laneOf(v, to)
}

// moveLane sends the focused column to the band dir away, focus following.
func (cascadeLayout) moveLane(w *Workspace, dir int) bool {
	if len(w.Columns) == 0 {
		return false
	}
	first, _ := w.bandBounds(w.band(w.Focus))
	if !w.shiftBand(w.Focus, w.Focus, dir, w.Focus-first) {
		return false
	}
	w.scroll()
	return true
}

// isolate gives a window about to go fullscreen a band of its own: a
// parentless float becomes a column first.
func (cascadeLayout) isolate(w *Workspace, id WindowID) bool {
	w.fullscreenFloatBand(id)
	if w.isFloat(id) || w.fullscreen == id {
		return false
	}
	return w.fullscreenBand(id)
}

func (cascadeLayout) previewLanes() bool    { return true }
func (cascadeLayout) newState() layoutState { return &cascadeState{} }

// cascadeState owns positional band boundaries and per-column push history.
//
// Bands are explicit: bandStart marks the first column of each band, and moves
// keep the other bands as they are. A band also ends where it would pass
// MaxColumns columns (max-columns shrank, or a column was inserted into a full
// band), and a maximized column sits alone in its band. markBands freezes
// those ends into bandStart, so the bands stay when max-columns grows again or
// the column is unmaximized. Cells only share a band's width (laneWidth); they
// never move a column to a band.
type cascadeState struct {
	bandStart []bool
	cols      map[uint64]*cascadeCol // keyed by Column.key
}
type cascadeCol struct {
	pushedUp, pushedDown [][]WindowID
	ownBand              bool
}

// cascadeState is the workspace's cascade state. Only cascade code calls
// it; any other state is left alone and a detached one is returned.
func (w *Workspace) cascadeState() *cascadeState {
	if s, ok := w.state().(*cascadeState); ok {
		return s
	}
	return &cascadeState{}
}
func (w *Workspace) bandStart(i int) bool {
	s, ok := w.lstate.(*cascadeState)
	return ok && i >= 0 && i < len(s.bandStart) && s.bandStart[i]
}
func (w *Workspace) setBandStart(i int, start bool) {
	s := w.cascadeState()
	if len(s.bandStart) < len(w.Columns) {
		s.bandStart = append(s.bandStart, make([]bool, len(w.Columns)-len(s.bandStart))...)
	}
	s.bandStart[i] = start
}
func (w *Workspace) cascadeCol(c Column) *cascadeCol {
	s := w.cascadeState()
	if s.cols == nil {
		s.cols = make(map[uint64]*cascadeCol)
	}
	if s.cols[c.key] == nil {
		s.cols[c.key] = &cascadeCol{}
	}
	return s.cols[c.key]
}

func (s *cascadeState) inserted(w *Workspace, at int, kind insertKind, carry any) any {
	for len(s.bandStart) < len(w.Columns)-1 {
		s.bandStart = append(s.bandStart, false)
	}
	s.bandStart = slices.Insert(s.bandStart, at, false)
	switch kind {
	case insertNewLane:
		s.bandStart[at] = true
	case insertJoinDisplaced:
		// The new column takes the band start of the one it displaces.
		if at+1 < len(s.bandStart) && s.bandStart[at+1] {
			s.bandStart[at], s.bandStart[at+1] = true, false
		}
	}
	if c, ok := carry.(*cascadeCol); ok && c != nil {
		if s.cols == nil {
			s.cols = make(map[uint64]*cascadeCol)
		}
		s.cols[w.Columns[at].key] = c
		return nil
	}
	return carry
}
func (s *cascadeState) removed(at int, col Column) any {
	if at < len(s.bandStart) {
		start := s.bandStart[at]
		s.bandStart = slices.Delete(s.bandStart, at, at+1)
		if start && at < len(s.bandStart) {
			s.bandStart[at] = true
		}
	}
	c := s.cols[col.key]
	delete(s.cols, col.key)
	if c == nil {
		return nil
	}
	return c
}
func (*cascadeState) settle(w *Workspace) { w.markBands(); w.dropStaleSpans() }
