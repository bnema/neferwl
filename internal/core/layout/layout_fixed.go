package layout

// OverflowFixed keeps every column on screen: past the max, each new column
// splits the newest one, alternating top/bottom and left/right (dwindle
// spiral).
const OverflowFixed Overflow = "fixed"

// fixedLayout keeps every column on screen: past the max, each new column
// splits the newest one, alternating top/bottom and left/right (dwindle
// spiral).
type fixedLayout struct{ Base }

// Name returns the configuration key.
func (fixedLayout) Name() Overflow { return OverflowFixed }

// EqualCells reports whether lanes share width by cells.
func (fixedLayout) EqualCells() bool { return true }

// MaximizeHides reports whether maximization hides other columns.
func (fixedLayout) MaximizeHides() bool { return true }

// PinsFullscreen reports whether fullscreen immediately covers the output.
func (fixedLayout) PinsFullscreen() bool { return true }

// ResetsMaximize reports whether switching layouts clears maximization.
func (fixedLayout) ResetsMaximize() bool { return true }

// Spiral reports whether excess columns subdivide the last slot.
func (fixedLayout) Spiral() bool { return true }

// Scrolls reports whether the content axis can slide.
func (fixedLayout) Scrolls() bool { return false }

// ScrollTarget returns the settled offset that shows the selected column.
func (fixedLayout) ScrollTarget(View) int { return 0 }

// ColumnRects is the row of columns, with the spiral past MaxColumns.
func (l fixedLayout) ColumnRects(v View, dst []Rect, o RectOpts) []Rect {
	rects := l.Base.ColumnRects(v, dst, o)
	n, g := len(v.Columns), v.Gap
	if !o.IgnoreFullWidth && n > 0 && v.Columns[v.Focus].FullWidth {
		rects[v.Focus].X = v.Usable.X + g
		return rects
	}
	k := max(v.MaxColumns, 1)
	if n <= k {
		return rects
	}
	// The last slot holds the spiral; the columns before it keep their place.
	area := rects[k-1]
	for i := k - 1; i < n-1; i++ {
		rects[i], area = Split(area, g, (i-k+1)%2 == 0)
	}
	rects[n-1] = area
	return rects
}

// LaneRow reports whether columns share a row by cells.
func (fixedLayout) LaneRow(v View) bool { return len(v.Columns) <= max(v.MaxColumns, 1) }

// FocusStep selects a column along the content axis, or -1 at an edge.
func (l fixedLayout) FocusStep(v View, dir int) int {
	if len(v.Columns) == 0 || (dir != -1 && dir != 1) {
		return -1
	}
	if ScreenFocus(v) {
		return ScreenNeighbor(v, v.Layout.ColumnRects(v, nil, RectOpts{}), dir, 0)
	}
	return l.Base.FocusStep(v, dir)
}

// LaneStep selects a column across lanes, or -1 when none exists.
func (fixedLayout) LaneStep(v View, dir int) int {
	if !ScreenFocus(v) {
		return -1
	}
	return ScreenNeighbor(v, v.Layout.ColumnRects(v, nil, RectOpts{}), 0, dir)
}

// SpawnAt returns the insertion position and lane relationship of a new column.
func (fixedLayout) SpawnAt(v View) (int, InsertKind) { return len(v.Columns), InsertDefault }

// CoveredSpawnAt returns the insertion position under a covering window.
func (fixedLayout) CoveredSpawnAt(v View) int { return len(v.Columns) }

// Full reports whether a new column must be expelled to a neighbor.
func (fixedLayout) Full(v View) bool { return len(v.Columns) >= max(v.MaxColumns, 1) }

// PreviewTiles projects columns for overview, reusing the supplied buffers.
func (fixedLayout) PreviewTiles(v View, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	w := &v
	rects := v.Layout.ColumnRects(v, rowBuf[:0], RectOpts{})
	rowBuf = rects[len(rects):cap(rects)]
	var sel Rect
	for i, c := range v.Columns {
		if w.hiddenByMaximized(i) {
			for _, id := range c.Windows {
				dst = append(dst, Placement{ID: id, Hidden: true})
			}
			continue
		}
		r := rects[i]
		r.X, r.Y = r.X-v.Usable.X, r.Y-v.Usable.Y
		dst = appendPreviewColumn(dst, rowBuf, r, c, v.Gap, i == v.Focus)
		if i == v.Focus {
			sel = r
		}
	}
	return dst, v.Usable.W, sel
}
