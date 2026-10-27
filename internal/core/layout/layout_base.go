package layout

import "slices"

// Base is the neutral single-lane column layout: the defaults every
// layout embeds and overrides where it differs. Methods that need another
// layout method call it through v.Layout, so an embedding layout's override
// is honored.
type Base struct{}

// Name returns the configuration key.
func (Base) Name() Overflow { return Default }

// Axes returns the content and workspace navigation axes.
func (Base) Axes() (Axis, Axis) { return Horizontal, Vertical }

// EqualCells reports whether lanes share width by cells.
func (Base) EqualCells() bool { return false }

// MaximizeHides reports whether maximization hides other columns.
func (Base) MaximizeHides() bool { return false }

// Scrolls reports whether the content axis can slide.
func (Base) Scrolls() bool { return true }

// PinsFullscreen reports whether fullscreen immediately covers the output.
func (Base) PinsFullscreen() bool { return false }

// ResetsMaximize reports whether switching layouts clears maximization.
func (Base) ResetsMaximize() bool { return false }

// OverridesWidth enables dispatch to the engine’s custom column widths.
func (Base) OverridesWidth() bool { return false }

// NewState constructs independent per-owner state, or nil.
func (Base) NewState() State { return nil }

// Bands reports whether columns group into independent bands.
func (Base) Bands() bool { return false }

// LaneGestures reports whether content swipes step between lanes.
func (Base) LaneGestures() bool { return false }

// Spiral reports whether excess columns subdivide the last slot.
func (Base) Spiral() bool { return false }

// ColumnRects appends screen-space column rectangles using the supplied storage.
func (Base) ColumnRects(v View, dst []Rect, o RectOpts) []Rect {
	w := &v
	g := w.gap()
	y, h := w.Usable.Y+g, max(w.Usable.H-2*g, 0)
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + o.Shift
	x := w.Usable.X + g
	v = v.WithFacts()
	if !v.Layout.OverridesWidth() {
		// Resolve native geometry once, without copying a view per column.
		native := newNativeWidths(w, v.facts)
		for i := range w.Columns {
			width := nativeColumnWidth(&v, &native, i, o.IgnoreFullWidth)
			rects[i] = Rect{X: x - view, Y: y, W: width, H: h}
			x += width + g
		}
		return rects
	}
	for i := range w.Columns {
		width := v.Layout.ColumnWidth(v, i, o.IgnoreFullWidth)
		rects[i] = Rect{X: x - view, Y: y, W: width, H: h}
		x += width + g
	}
	return rects
}

// ColumnWidth returns a column’s width in logical pixels.
func (Base) ColumnWidth(v View, i int, ignore bool) int {
	w := &v
	if !v.facts.set {
		v = v.WithFacts()
	}
	row := newNativeWidths(w, v.facts)
	return nativeColumnWidth(&v, &row, i, ignore)
}

func nativeColumnWidth(v *View, row *nativeWidths, i int, ignore bool) int {
	f := v.facts
	if !f.set {
		w := v
		if !f.set {
			f = w.WidthFactsOf() // called alone: the loops set them once
		}
		return w.ColumnWidthWith(i, ignore, f)
	}
	if f.laneRow {
		return row.width(i, ignore)
	}
	c := &v.Columns[i]
	if c.FullWidth && !ignore {
		return max(v.Usable.W-2*v.Gap, 0)
	}
	if fullscreenColumn(*c, v.Fullscreen) {
		return v.Output.W
	}
	if f.spiral {
		return int(f.slot) // Past MaxColumns the spiral's slots.
	}
	if f.spansValid {
		return c.Span * max(v.Usable.W-2*v.Gap, 0) / 10000
	}
	if c.Cells <= 0 {
		return int(f.auto)
	}
	n := min(c.Cells, max(v.MaxColumns, 1))
	return max(n*int(f.slot)+(n-1)*v.Gap, 0)
}

// ScrollTarget returns the settled offset that shows the selected column.
func (Base) ScrollTarget(v View) int {
	w := &v
	if len(w.Columns) == 0 {
		return w.View
	}
	vf := v.WithFacts()
	left := vf.ColumnX(w.Focus) - w.View
	width := vf.Layout.ColumnWidth(vf, w.Focus, false)
	if w.fullscreenColumn(w.Focus) {
		w.View += left - w.Output.X
		return w.View
	}
	minX := w.Usable.X + w.gap()
	maxX := w.Usable.X + w.Usable.W - w.gap()
	if width > maxX-minX || left < minX {
		w.View += left - minX
	} else if left+width > maxX {
		w.View += left + width - maxX
	}
	return w.View
}

// SnapPoints builds sorted gesture destinations in dst[:0].
func (Base) SnapPoints(v View, dst []float64) []float64 {
	return scrollSnapPoints(v, dst)
}

// SnapFocus selects the column at a gesture destination.
func (Base) SnapFocus(v View, view int, forward bool) int {
	return scrollSnapFocus(v, view, forward)
}

// LaneOf returns the lane containing a column.
func (Base) LaneOf(View, int) int { return 0 }

// LaneBounds returns inclusive column bounds; first > last denotes no lane.
func (Base) LaneBounds(v View, lane int) (int, int) {
	if lane != 0 || len(v.Columns) == 0 {
		return 0, -1
	}
	return 0, len(v.Columns) - 1
}

// LaneCount returns the number of lanes.
func (Base) LaneCount(v View) int {
	if len(v.Columns) == 0 {
		return 0
	}
	return 1
}

// LaneNeighbor selects the closest column in an adjacent lane, or -1.
func (Base) LaneNeighbor(View, int) int { return -1 }

// LaneRow reports whether columns share a row by cells.
func (Base) LaneRow(View) bool { return false }

// FocusStep selects a column along the content axis, or -1 at an edge.
func (Base) FocusStep(v View, dir int) int {
	i := v.Focus + dir
	if len(v.Columns) == 0 || (dir != -1 && dir != 1) || i < 0 || i >= len(v.Columns) {
		return -1
	}
	return i
}

// LaneStep selects a column across lanes, or -1 when none exists.
func (Base) LaneStep(View, int) int { return -1 }

// SpawnAt returns the insertion position and lane relationship of a new column.
func (Base) SpawnAt(v View) (int, InsertKind) {
	return min(v.Focus+1, len(v.Columns)), InsertDefault
}

// CoveredSpawnAt returns the insertion position under a covering window.
func (Base) CoveredSpawnAt(v View) int { return min(v.Focus+1, len(v.Columns)) }

// Full reports whether a new column must be expelled to a neighbor.
func (Base) Full(View) bool { return false }

// MoveAllowed reports whether columns may swap positions.
func (Base) MoveAllowed(v View, from, to int) bool {
	return from >= 0 && to >= 0 && from < len(v.Columns) && to < len(v.Columns)
}

// MoveLane moves a window to the lane in dir and reports a change.
func (Base) MoveLane(Edit, int) bool { return false }

// Isolate gives a window its own lane before fullscreen.
func (Base) Isolate(Edit, WindowID) bool { return false }

// PreviewTiles projects columns for overview, reusing the supplied buffers.
func (Base) PreviewTiles(v View, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	// The unscrolled row, through the resolved layout so an override of its
	// geometry is honored; the row scratch follows the rects in rowBuf.
	v0 := v
	v0.View = 0
	rects := v.Layout.ColumnRects(v0, rowBuf[:0], RectOpts{})
	rowBuf = rects[len(rects):cap(rects)]
	span := 0
	var sel Rect
	for i, c := range v.Columns {
		r := rects[i]
		r.X, r.Y = r.X-v.Usable.X, v.Gap
		if fullscreenColumn(c, v.Fullscreen) {
			r.W = max(v.Usable.W-2*v.Gap, 0)
		}
		dst = appendPreviewColumn(dst, rowBuf, r, c, v.Gap, i == v.Focus)
		span = max(span, r.X+r.W+v.Gap)
		if i == v.Focus {
			sel = r
		}
	}
	return dst, span, sel
}

// PreviewLanes reports whether preview Y coordinates encode lanes.
func (Base) PreviewLanes() bool { return false }

// AdoptFloat isolates a formerly floating window in a tiled lane.
func (Base) AdoptFloat(Edit, WindowID) bool { return false }
