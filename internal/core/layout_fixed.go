package core

// OverflowFixed keeps every column on screen: past the max, each new column
// splits the newest one, alternating top/bottom and left/right (dwindle
// spiral).
const OverflowFixed Overflow = "fixed"

// fixedLayout keeps every column on screen: past the max, each new column
// splits the newest one, alternating top/bottom and left/right (dwindle
// spiral).
type fixedLayout struct{ columnsBase }

func (fixedLayout) name() Overflow              { return OverflowFixed }
func (fixedLayout) equalCells() bool            { return true }
func (fixedLayout) maximizeHides() bool         { return true }
func (fixedLayout) pinsFullscreen() bool        { return true }
func (fixedLayout) resetsMaximize() bool        { return true }
func (fixedLayout) spiral() bool                { return true }
func (fixedLayout) scrolls() bool               { return false }
func (fixedLayout) scrollTarget(layoutView) int { return 0 }

// columnRects is the row of columns, with the spiral past MaxColumns. The
// post-pass reads the snapshot directly: the base already built the one
// Workspace adapter this call needs.
func (l fixedLayout) columnRects(v layoutView, dst []Rect, o rectOpts) []Rect {
	rects := l.columnsBase.columnRects(v, dst, o)
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
		rects[i], area = split(area, g, (i-k+1)%2 == 0)
	}
	rects[n-1] = area
	return rects
}

func (fixedLayout) laneRow(v layoutView) bool { return len(v.Columns) <= max(v.MaxColumns, 1) }

func (l fixedLayout) focusStep(v layoutView, dir int) int {
	if len(v.Columns) == 0 || (dir != -1 && dir != 1) {
		return -1
	}
	if screenFocus(v) {
		return screenNeighbor(v, v.layout.columnRects(v, nil, rectOpts{}), dir, 0)
	}
	return l.columnsBase.focusStep(v, dir)
}
func (fixedLayout) laneStep(v layoutView, dir int) int {
	if !screenFocus(v) {
		return -1
	}
	return screenNeighbor(v, v.layout.columnRects(v, nil, rectOpts{}), 0, dir)
}

func (fixedLayout) spawnAt(v layoutView) (int, insertKind) { return len(v.Columns), insertDefault }
func (fixedLayout) coveredSpawnAt(v layoutView) int        { return len(v.Columns) }
func (fixedLayout) full(v layoutView) bool                 { return len(v.Columns) >= max(v.MaxColumns, 1) }

func (fixedLayout) previewTiles(v layoutView, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	w := geometryWorkspace(v)
	rects := v.layout.columnRects(v, rowBuf[:0], rectOpts{})
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
