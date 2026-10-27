package layout

import "slices"

// OverflowCascade wraps columns into vertically scrolling viewport bands.
const OverflowCascade Overflow = "cascade"

// cascadeLayout wraps columns into vertically scrolling viewport bands.
type cascadeLayout struct{ Base }

// Name returns the configuration key.
func (cascadeLayout) Name() Overflow { return OverflowCascade }

// EqualCells reports whether lanes share width by cells.
func (cascadeLayout) EqualCells() bool { return true }

// PinsFullscreen reports whether fullscreen immediately covers the output.
func (cascadeLayout) PinsFullscreen() bool { return true }

// ResetsMaximize reports whether switching layouts clears maximization.
func (cascadeLayout) ResetsMaximize() bool { return true }

// Bands reports whether columns group into independent bands.
func (cascadeLayout) Bands() bool { return true }

// LaneGestures reports whether content swipes step between lanes.
func (cascadeLayout) LaneGestures() bool { return true }

// LaneRow reports whether columns share a row by cells.
func (cascadeLayout) LaneRow(View) bool { return true }

// ColumnRects appends screen-space column rectangles using the supplied storage.
func (cascadeLayout) ColumnRects(v View, dst []Rect, o RectOpts) []Rect {
	w := &v
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

// SnapPoints builds sorted gesture destinations in dst[:0].
func (cascadeLayout) SnapPoints(v View, dst []float64) []float64 {
	w := &v

	n, spacing := w.cascadeBands()
	points := slices.Grow(dst[:0], n)[:n]
	for i := range points {
		points[i] = float64(i * spacing)
	}
	return points
}

// SnapFocus selects the column at a gesture destination.
func (cascadeLayout) SnapFocus(v View, view int, forward bool) int {
	w := &v

	if len(w.Columns) == 0 {
		return -1
	}
	b := min(max(view/max(w.Usable.H, 1), 0), w.band(len(w.Columns)-1))
	return w.laneNeighbor(b - w.band(w.Focus))
}

// LaneOf returns the lane containing a column.
func (cascadeLayout) LaneOf(v View, i int) int {
	w := &v
	return w.band(i)
}

// LaneBounds returns inclusive column bounds; first > last denotes no lane.
func (cascadeLayout) LaneBounds(v View, lane int) (int, int) {
	w := &v
	return w.bandBounds(lane)
}

// LaneNeighbor selects the closest column in an adjacent lane, or -1.
func (cascadeLayout) LaneNeighbor(v View, d int) int {
	return adjacentLane(v.Layout, v, d)
}

// ScrollTarget returns the settled offset that shows the selected column.
func (cascadeLayout) ScrollTarget(v View) int {
	w := &v
	return w.band(v.Focus) * v.Usable.H
}

// FocusStep selects a column along the content axis, or -1 at an edge.
func (l cascadeLayout) FocusStep(v View, dir int) int {
	i := l.Base.FocusStep(v, dir)
	if i < 0 || v.Layout.LaneOf(v, i) != v.Layout.LaneOf(v, v.Focus) {
		return -1
	}
	return i
}

// CoveredSpawnAt returns the insertion position under a covering window.
func (cascadeLayout) CoveredSpawnAt(v View) int { return len(v.Columns) }

// PreviewTiles projects columns for overview, reusing the supplied buffers.
func (cascadeLayout) PreviewTiles(v View, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	w := &v
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

// Axes returns the content and workspace navigation axes.
func (cascadeLayout) Axes() (Axis, Axis) { return Vertical, Horizontal }

// LaneCount returns the number of lanes.
func (cascadeLayout) LaneCount(v View) int {
	if len(v.Columns) == 0 {
		return 0
	}
	w := &v
	return w.band(len(v.Columns)-1) + 1
}

// LaneStep selects a column across lanes, or -1 when none exists.
func (cascadeLayout) LaneStep(v View, dir int) int { return v.Layout.LaneNeighbor(v, dir) }

// SpawnAt returns the insertion position and lane relationship of a new column.
func (cascadeLayout) SpawnAt(v View) (int, InsertKind) {
	if len(v.Columns) == 0 {
		return 0, InsertNewLane
	}
	first, last := v.Layout.LaneBounds(v, v.Layout.LaneOf(v, v.Focus))
	full := last-first+1 >= max(v.MaxColumns, 1)
	for _, c := range v.Columns[first : last+1] {
		full = full || c.FullWidth
	}
	if full {
		return last + 1, InsertNewLane
	}
	return last + 1, InsertDefault
}

// MoveAllowed reports whether columns may swap positions.
func (cascadeLayout) MoveAllowed(v View, from, to int) bool {
	if from < 0 || to < 0 || from >= len(v.Columns) || to >= len(v.Columns) {
		return false
	}
	return v.Layout.LaneOf(v, from) == v.Layout.LaneOf(v, to)
}

// MoveLane sends the focused column to the band dir away, focus following.
func (cascadeLayout) MoveLane(w Edit, dir int) bool {
	if len(w.Columns) == 0 {
		return false
	}
	first, _ := w.bandBounds(w.Band(w.Focus))
	return w.shiftBand(w.Focus, w.Focus, dir, w.Focus-first)
}

// Isolate gives a window about to go fullscreen a band of its own: a
// parentless float becomes a column first.
func (cascadeLayout) Isolate(w Edit, id WindowID) bool {
	return w.fullscreenBand(id)
}

// PreviewLanes reports whether preview Y coordinates encode lanes.
func (cascadeLayout) PreviewLanes() bool { return true }

// NewState constructs independent per-owner state, or nil.
func (cascadeLayout) NewState() State { return &cascadeState{} }

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
	cols      map[uint64]*cascadeCol // keyed by Column.Key
}
type cascadeCol struct {
	pushedUp, pushedDown [][]WindowID
	ownBand              bool
}

// cascadeState is the workspace's cascade state. Only cascade code calls
// it; any other state is left alone and a detached one is returned.
func (w *Lanes) cascadeState() *cascadeState {
	if s, ok := w.state().(*cascadeState); ok {
		return s
	}
	return &cascadeState{}
}
func (w *View) bandStart(i int) bool {
	s, ok := w.State.(*cascadeState)
	return ok && i >= 0 && i < len(s.bandStart) && s.bandStart[i]
}

// BandStartCount reports how many band boundaries the cascade state stores,
// and false when the workspace holds no cascade state. It is a read-only
// diagnostic for tests of the invariant that one boundary exists per column;
// it never builds the state.
func (w *Lanes) BandStartCount() (n int, ok bool) {
	s, ok := w.lstate.(*cascadeState)
	if !ok {
		return 0, false
	}
	return len(s.bandStart), true
}

// SetBandStart sets an explicit cascade boundary for column i. Other layouts
// do not retain cascade boundaries.
func (w *Lanes) SetBandStart(i int, start bool) {
	s := w.cascadeState()
	if len(s.bandStart) < len(w.Columns) {
		s.bandStart = append(s.bandStart, make([]bool, len(w.Columns)-len(s.bandStart))...)
	}
	s.bandStart[i] = start
}
func (w *Lanes) cascadeCol(c Column) *cascadeCol {
	s := w.cascadeState()
	if s.cols == nil {
		s.cols = make(map[uint64]*cascadeCol)
	}
	if s.cols[c.Key] == nil {
		s.cols[c.Key] = &cascadeCol{}
	}
	return s.cols[c.Key]
}

// Inserted updates band boundaries and consumes cascade carries.
func (s *cascadeState) Inserted(w *Lanes, at int, kind InsertKind, carry any) any {
	for len(s.bandStart) < len(w.Columns)-1 {
		s.bandStart = append(s.bandStart, false)
	}
	s.bandStart = slices.Insert(s.bandStart, at, false)
	switch kind {
	case InsertNewLane:
		s.bandStart[at] = true
	case InsertJoinDisplaced:
		// The new column takes the band start of the one it displaces.
		if at+1 < len(s.bandStart) && s.bandStart[at+1] {
			s.bandStart[at], s.bandStart[at+1] = true, false
		}
	}
	if c, ok := carry.(*cascadeCol); ok && c != nil {
		if s.cols == nil {
			s.cols = make(map[uint64]*cascadeCol)
		}
		s.cols[w.Columns[at].Key] = c
		return nil
	}
	return carry
}

// Removed returns the column’s cascade history for its next owner.
func (s *cascadeState) Removed(at int, col Column) any {
	if at < len(s.bandStart) {
		start := s.bandStart[at]
		s.bandStart = slices.Delete(s.bandStart, at, at+1)
		if start && at < len(s.bandStart) {
			s.bandStart[at] = true
		}
	}
	c := s.cols[col.Key]
	delete(s.cols, col.Key)
	if c == nil {
		return nil
	}
	return c
}

// Settle refreshes layout state after column edits, before measuring.
func (*cascadeState) Settle(w *Lanes) { w.markBands(); w.DropStaleSpans() }
