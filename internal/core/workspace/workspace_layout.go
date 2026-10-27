package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// sizable reports whether the focused column can be resized: a tiled column
// the fullscreen window does not hide.
func (w *Workspace) sizable() bool {
	return !w.OnFloat() && len(w.Columns) > 0 && !w.FullscreenHides()
}

func (w *Workspace) edit() layout.Edit {
	id, _ := w.Focused()
	return layout.Edit{Lanes: &w.Lanes, Focused: id}
}

func (w *Workspace) isolate(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 && w.Floats[i].over == 0 && w.StashIndex(id) < 0 {
		if e := w.edit(); w.Engine().AdoptFloat(e, id) {
			w.Floats = slices.Delete(w.Floats, i, i+1)
			if e.Focused == id {
				w.floatFocus, w.stashFocus = false, false
			}
		}
	}
	if w.IsFloat(id) || w.Fullscreen == id {
		return false
	}
	return w.Engine().Isolate(w.edit(), id)
}

func (w *Workspace) columnWidthFor(i int, ignoreFullWidth bool) int {
	return w.Engine().ColumnWidth(w.Snapshot(), i, ignoreFullWidth)
}

func (w *Workspace) columnX(i int) int {
	v := w.Snapshot()
	v = v.WithFacts()
	return v.ColumnX(i)
}

func (w *Workspace) scroll() {
	w.SettleLayout()
	before := w.View
	w.View = w.Engine().ScrollTarget(w.Snapshot())
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
	return w.Engine().ColumnRects(w.Snapshot(), dst, layout.RectOpts{IgnoreFullWidth: ignoreFullWidth, Shift: shift})
}

// FocusLane focuses the column in the lane dir away; a maximized column
// keeps its own band, so it stays maximized.
func (w *Workspace) FocusLane(dir int) bool {
	i := w.LaneNeighbor(dir)
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
