package core

func (w *Workspace) columnWidthFor(i int, ignoreFullWidth bool) int {
	return w.layout().columnWidth(w.lview(), i, ignoreFullWidth)
}

// scrollSpansValid is the lane validity scrollWidth needs. It costs O(n):
// loops over columns compute it once and pass it down.
func (w *Workspace) scrollSpansValid() bool {
	return !w.layout().equalCells() && w.validSpans(0, len(w.Columns)-1)
}

// widthFacts are what column widths need to know about the whole workspace,
// the same for every column: loops compute them once and pass them down. They
// are small: a view carrying them is copied on every layout call.
type widthFacts struct {
	slot       int32 // equalShare(MaxColumns): the width of one cell
	auto       int32 // equalShare(min(columns, MaxColumns)): an auto column
	set        bool  // computed; the zero value is "not computed"
	spansValid bool  // the scroll lane's manual widths are complete
	laneRow    bool  // the columns share one row of cells
	spiral     bool  // past the row, the last slot subdivides
}

// widthFactsOf is widthFacts for a view the caller already built.
func (w *Workspace) widthFactsOf(v layoutView) widthFacts {
	k := max(w.MaxColumns, 1)
	return widthFacts{set: true, spansValid: w.scrollSpansValid(), laneRow: v.layout.laneRow(v), spiral: v.layout.spiral(),
		slot: int32(w.equalShare(k)), auto: int32(w.equalShare(min(len(w.Columns), k)))}
}

// withFacts is v with its width facts set; w is geometryWorkspace(v).
func (v layoutView) withFacts(w *Workspace) layoutView {
	v.facts = w.widthFactsOf(v)
	return v
}

// columnWidthWith is columnWidthFor with the workspace's width facts given.
func (w *Workspace) columnWidthWith(i int, ignoreFullWidth bool, f widthFacts) int {
	if width, ok := w.specialWidth(i, ignoreFullWidth); ok {
		return width
	}
	if f.laneRow {
		return w.cellWidth(i)
	}
	return w.scrollWidth(i, f)
}

// specialWidth is the width of a maximized or fullscreen column.
func (w *Workspace) specialWidth(i int, ignoreFullWidth bool) (int, bool) {
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
func (w *Workspace) laneRow() bool {
	return w.layout().laneRow(w.lview())
}

// inSpiral reports whether the spiral applies: more columns than the row
// holds, in a layout that subdivides its last slot.
func (w *Workspace) inSpiral() bool { return w.layout().spiral() && !w.laneRow() }

// scrollWidth is the width of column i when it is sized on its own: auto
// columns share the usable width as the visible ones would, the others take
// their cells (at most MaxColumns) and the gaps between them.
func (w *Workspace) scrollWidth(i int, f widthFacts) int {
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
func (w *Workspace) columnEdges() (xs, widths []int) {
	n := len(w.Columns)
	buf := make([]int, 2*n)
	xs, widths = buf[:n:n], buf[n:]
	v := w.lview()
	v = v.withFacts(w)
	x := w.Usable.X + w.gap()
	native := newNativeWidths(w, v.facts)
	override := v.layout.overridesWidth()
	for i := range w.Columns {
		xs[i] = x
		if override {
			widths[i] = v.layout.columnWidth(v, i, false)
		} else {
			widths[i] = nativeColumnWidth(&v, &native, i, false)
		}
		x += widths[i] + w.gap()
	}
	return xs, widths
}

func (w *Workspace) columnX(i int) int {
	v := w.lview()
	v = v.withFacts(w)
	return v.columnX(i)
}

// columnX is the left edge of column i in the unscrolled row; v carries facts.
func (v layoutView) columnX(i int) int {
	x := v.Usable.X + v.Gap
	w := geometryWorkspace(v)
	native := newNativeWidths(&w, v.facts)
	override := v.layout.overridesWidth()
	for j := 0; j < i; j++ {
		if override {
			x += v.layout.columnWidth(v, j, false) + v.Gap
		} else {
			x += nativeColumnWidth(&v, &native, j, false) + v.Gap
		}
	}
	return x
}

func (w *Workspace) scroll() {
	w.settleLayout()
	before := w.View
	w.View = w.layout().scrollTarget(w.lview())
	w.retarget(before)
}

// columnRects returns each column's area on screen, before stacking windows.
func (w *Workspace) columnRects() []Rect {
	return w.columnRectsInto(nil, false)
}

// columnRectsFor shares the fixed layout geometry with the overview's hidden
// columns card, without changing FullWidth or the clients' saved buffers.
func (w *Workspace) columnRectsFor(ignoreFullWidth bool) []Rect {
	return w.columnRectsInto(nil, ignoreFullWidth)
}

// columnRectsInto is columnRectsFor built in dst's storage (dst[:0] grown
// when too small): the result aliases dst, so a caller passing a reused
// buffer must be done with the previous result.
func (w *Workspace) columnRectsInto(dst []Rect, ignoreFullWidth bool) []Rect {
	return w.columnRectsAt(dst, ignoreFullWidth, w.shiftPixels())
}

// columnRectsAt is columnRectsInto with the view slide's shift given: 0 is
// the settled geometry, without the animation's presentation offset.
func (w *Workspace) columnRectsAt(dst []Rect, ignoreFullWidth bool, shift int) []Rect {
	return w.layout().columnRects(w.lview(), dst, rectOpts{IgnoreFullWidth: ignoreFullWidth, Shift: shift})
}

// nativeWidths keeps the adapter and row statistics outside column loops.
// Overrides still receive the complete snapshot through the layout interface.
type nativeWidths struct {
	w         *Workspace
	facts     widthFacts
	S, u, sum int
	spans     bool
}

func newNativeWidths(w *Workspace, f widthFacts) nativeWidths {
	n := nativeWidths{w: w, facts: f}
	if f.laneRow && !w.layout().bands() {
		n.S, n.u, n.sum = w.laneStats(0, len(w.Columns)-1)
		n.spans = w.validSpans(0, len(w.Columns)-1)
	}
	return n
}

func (n nativeWidths) width(i int, ignore bool) int {
	if width, ok := n.w.specialWidth(i, ignore); ok {
		return width
	}
	if n.facts.laneRow {
		if n.w.layout().bands() {
			return n.w.cellWidth(i)
		}
		return n.w.laneWidth(i, 0, len(n.w.Columns)-1, n.S, n.u, n.sum, n.spans)
	}
	return n.w.scrollWidth(i, n.facts)
}
