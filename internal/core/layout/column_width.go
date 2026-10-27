package layout

// ScrollSpansValid is the lane validity scrollWidth needs. It costs O(n):
// loops over columns compute it once and pass it down.
func (w *View) ScrollSpansValid() bool {
	return !w.Layout.EqualCells() && w.validSpans(0, len(w.Columns)-1)
}

// WidthFacts are what column widths need to know about the whole workspace,
// the same for every column: loops compute them once and pass them down. They
// are small: a view carrying them is copied on every layout call.
type WidthFacts struct {
	slot       int32 // equalShare(MaxColumns): the width of one cell
	auto       int32 // equalShare(min(columns, MaxColumns)): an auto column
	set        bool  // computed; the zero value is "not computed"
	spansValid bool  // the scroll lane's manual widths are complete
	laneRow    bool  // the columns share one row of cells
	spiral     bool  // past the row, the last slot subdivides
}

// WidthFactsOf is widthFacts for a view the caller already built.
func (w *View) WidthFactsOf() WidthFacts {
	k := max(w.MaxColumns, 1)
	return WidthFacts{set: true, spansValid: w.ScrollSpansValid(), laneRow: w.Layout.LaneRow(*w), spiral: w.Layout.Spiral(),
		slot: int32(w.equalShare(k)), auto: int32(w.equalShare(min(len(w.Columns), k)))}
}

// WithFacts is v with its width facts set.
func (v View) WithFacts() View {
	v.facts = v.WidthFactsOf()
	return v
}

// ColumnWidthWith is columnWidthFor with the workspace's width facts given.
func (w *View) ColumnWidthWith(i int, ignoreFullWidth bool, f WidthFacts) int {
	if width, ok := w.specialWidth(i, ignoreFullWidth); ok {
		return width
	}
	if f.laneRow {
		return w.cellWidth(i)
	}
	return w.ScrollWidth(i, f)
}

// specialWidth is the width of a maximized or fullscreen column.
func (w *View) specialWidth(i int, ignoreFullWidth bool) (int, bool) {
	if w.Columns[i].FullWidth && !ignoreFullWidth {
		return max(w.Usable.W-2*w.gap(), 0), true
	}
	if w.fullscreenColumn(i) {
		return w.Output.W, true
	}
	return 0, false
}

// laneRow reports whether the columns share one row of cells: fixed
// overflow up to MaxColumns of them. Past that the spiral applies and
// cells are ignored.
func (w *View) laneRow() bool {
	return w.Layout.LaneRow(*w)
}

// inSpiral reports whether the spiral applies: more columns than the row
// holds, in a layout that subdivides its last slot.
func (w *View) inSpiral() bool { return w.Layout.Spiral() && !w.laneRow() }

// ScrollWidth is the width of column i when it is sized on its own: auto
// columns share the usable width as the visible ones would, the others take
// their cells (at most MaxColumns) and the gaps between them.
func (w *View) ScrollWidth(i int, f WidthFacts) int {
	k := max(w.MaxColumns, 1)
	if f.spiral {
		// Past MaxColumns the spiral's slots.
		return int(f.slot)
	}
	if f.spansValid {
		return w.Columns[i].Span * max(w.Usable.W-2*w.gap(), 0) / 10000
	}
	c := w.Columns[i].Cells
	if c <= 0 {
		return int(f.auto)
	}
	c = min(c, k)
	return max(c*int(f.slot)+(c-1)*w.gap(), 0)
}

// columnEdges returns the x and width of every column in one pass, the
// prefix sums columnX(i) and columnWidth(i) would each recompute in O(i).
// Both slices share one backing array. Widths come from the layout.
func (w *View) columnEdges() (xs, widths []int) {
	n := len(w.Columns)
	buf := make([]int, 2*n)
	xs, widths = buf[:n:n], buf[n:]
	v := w.WithFacts()
	x := w.Usable.X + w.gap()
	native := newNativeWidths(w, v.facts)
	override := v.Layout.OverridesWidth()
	for i := range w.Columns {
		xs[i] = x
		if override {
			widths[i] = v.Layout.ColumnWidth(v, i, false)
		} else {
			widths[i] = nativeColumnWidth(&v, &native, i, false)
		}
		x += widths[i] + w.gap()
	}
	return xs, widths
}

// ColumnX is the left edge of column i in the unscrolled row; v carries facts.
func (v View) ColumnX(i int) int {
	x := v.Usable.X + v.Gap
	w := &v
	native := newNativeWidths(w, v.facts)
	override := v.Layout.OverridesWidth()
	for j := 0; j < i; j++ {
		if override {
			x += v.Layout.ColumnWidth(v, j, false) + v.Gap
		} else {
			x += nativeColumnWidth(&v, &native, j, false) + v.Gap
		}
	}
	return x
}

// nativeWidths keeps the adapter and row statistics outside column loops.
// Overrides still receive the complete snapshot through the layout interface.
type nativeWidths struct {
	v         *View
	facts     WidthFacts
	S, u, sum int
	spans     bool
}

func newNativeWidths(w *View, f WidthFacts) nativeWidths {
	n := nativeWidths{v: w, facts: f}
	if f.laneRow && !w.Layout.Bands() {
		n.S, n.u, n.sum = w.laneStats(0, len(w.Columns)-1)
		n.spans = w.validSpans(0, len(w.Columns)-1)
	}
	return n
}

func (n nativeWidths) width(i int, ignore bool) int {
	if width, ok := n.v.specialWidth(i, ignore); ok {
		return width
	}
	if n.facts.laneRow {
		if n.v.Layout.Bands() {
			return n.v.cellWidth(i)
		}
		return n.v.laneWidth(i, 0, len(n.v.Columns)-1, n.S, n.u, n.sum, n.spans)
	}
	return n.v.ScrollWidth(i, n.facts)
}
