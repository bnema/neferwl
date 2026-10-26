package core

import "slices"

// columnsBase is the neutral single-lane column layout: the defaults every
// layout embeds and overrides where it differs. Methods that need another
// layout method call it through v.layout, so an embedding layout's override
// is honored.
type columnsBase struct{}

func (columnsBase) name() Overflow                 { return defaultLayout }
func (columnsBase) axes() (layoutAxis, layoutAxis) { return horizontalAxis, verticalAxis }
func (columnsBase) equalCells() bool               { return false }
func (columnsBase) maximizeHides() bool            { return false }
func (columnsBase) scrolls() bool                  { return true }
func (columnsBase) pinsFullscreen() bool           { return false }
func (columnsBase) resetsMaximize() bool           { return false }
func (columnsBase) overridesWidth() bool           { return false }
func (columnsBase) newState() layoutState          { return nil }

func (columnsBase) bands() bool        { return false }
func (columnsBase) laneGestures() bool { return false }
func (columnsBase) spiral() bool       { return false }

func (columnsBase) columnRects(v layoutView, dst []Rect, o rectOpts) []Rect {
	w := &v
	g := w.gap()
	y, h := w.Usable.Y+g, max(w.Usable.H-2*g, 0)
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + o.Shift
	x := w.Usable.X + g
	v = v.withFacts()
	if !v.layout.overridesWidth() {
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
		width := v.layout.columnWidth(v, i, o.IgnoreFullWidth)
		rects[i] = Rect{X: x - view, Y: y, W: width, H: h}
		x += width + g
	}
	return rects
}

func (columnsBase) columnWidth(v layoutView, i int, ignore bool) int {
	w := &v
	if !v.facts.set {
		v = v.withFacts()
	}
	row := newNativeWidths(w, v.facts)
	return nativeColumnWidth(&v, &row, i, ignore)
}

func nativeColumnWidth(v *layoutView, row *nativeWidths, i int, ignore bool) int {
	f := v.facts
	if !f.set {
		w := v
		if !f.set {
			f = w.widthFactsOf() // called alone: the loops set them once
		}
		return w.columnWidthWith(i, ignore, f)
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

func (columnsBase) scrollTarget(v layoutView) int {
	w := &v
	if len(w.Columns) == 0 {
		return w.View
	}
	vf := v.withFacts()
	left := vf.columnX(w.Focus) - w.View
	width := vf.layout.columnWidth(vf, w.Focus, false)
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

func (columnsBase) snapPoints(v layoutView, dst []float64) []float64 {
	return scrollSnapPoints(v, dst)
}
func (columnsBase) snapFocus(v layoutView, view int, forward bool) int {
	return scrollSnapFocus(v, view, forward)
}

func (columnsBase) laneOf(layoutView, int) int { return 0 }
func (columnsBase) laneBounds(v layoutView, lane int) (int, int) {
	if lane != 0 || len(v.Columns) == 0 {
		return 0, -1
	}
	return 0, len(v.Columns) - 1
}
func (columnsBase) laneCount(v layoutView) int {
	if len(v.Columns) == 0 {
		return 0
	}
	return 1
}
func (columnsBase) laneNeighbor(layoutView, int) int { return -1 }
func (columnsBase) laneRow(layoutView) bool          { return false }

func (columnsBase) focusStep(v layoutView, dir int) int {
	i := v.Focus + dir
	if len(v.Columns) == 0 || (dir != -1 && dir != 1) || i < 0 || i >= len(v.Columns) {
		return -1
	}
	return i
}
func (columnsBase) laneStep(layoutView, int) int { return -1 }

func (columnsBase) spawnAt(v layoutView) (int, insertKind) {
	return min(v.Focus+1, len(v.Columns)), insertDefault
}
func (columnsBase) coveredSpawnAt(v layoutView) int { return min(v.Focus+1, len(v.Columns)) }
func (columnsBase) full(layoutView) bool            { return false }
func (columnsBase) moveAllowed(v layoutView, from, to int) bool {
	return from >= 0 && to >= 0 && from < len(v.Columns) && to < len(v.Columns)
}
func (columnsBase) moveLane(edit, int) bool     { return false }
func (columnsBase) isolate(edit, WindowID) bool { return false }

func (columnsBase) previewTiles(v layoutView, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect) {
	if tiles, span, sel, ok := previewPinned(v, dst, rowBuf); ok {
		return tiles, span, sel
	}
	// The unscrolled row, through the resolved layout so an override of its
	// geometry is honored; the row scratch follows the rects in rowBuf.
	v0 := v
	v0.View = 0
	rects := v.layout.columnRects(v0, rowBuf[:0], rectOpts{})
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
func (columnsBase) previewLanes() bool { return false }

func (columnsBase) adoptFloat(edit, WindowID) bool { return false }
