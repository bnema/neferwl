package core

import (
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

func modelFloatIndex(w *Workspace, id WindowID) int {
	return slices.IndexFunc(w.Floats, func(f Float) bool { return f.ID == id })
}
func modelColumnRects(w *Workspace) []Rect { return modelColumnRectsFor(w, false) }
func modelColumnRectsFor(w *Workspace, ignore bool) []Rect {
	return modelColumnRectsAt(w, nil, ignore, int(math.Round(w.ColumnsPosition()-float64(w.View))))
}
func modelColumnRectsAt(w *Workspace, dst []Rect, ignore bool, shift int) []Rect {
	return w.Engine().ColumnRects(w.Snapshot(), dst, layout.RectOpts{IgnoreFullWidth: ignore, Shift: shift})
}
func modelColumnWidth(w *Workspace, i int) int { return w.Engine().ColumnWidth(w.Snapshot(), i, false) }
func modelColumnX(w *Workspace, i int) int     { v := w.Snapshot().WithFacts(); return v.ColumnX(i) }
func modelColumnWindows(w *Workspace) int {
	n := 0
	for _, c := range w.Columns {
		n += len(c.Windows)
	}
	return n
}
func modelPlaced(w *Workspace, id WindowID) (Rect, bool) {
	for _, p := range w.Layout() {
		if p.ID == id && !p.Hidden {
			return p.Rect, true
		}
	}
	return Rect{}, false
}
func modelFloatRect(w *Workspace, f Float) Rect { _, r, _ := w.FloatAt(f.ID); return r }
func modelStashOverCover(w *Workspace) bool {
	s := w.Inspect()
	return s.StashOver != 0 && s.StashOver == w.Cover() && !s.StashHidden && len(w.Stash) > 0
}
func modelStashFocused(w *Workspace) bool {
	s := w.Inspect()
	return (s.StashFocus || len(w.Columns) == 0) && !s.StashHidden && len(w.Stash) > 0
}
func modelNoteFocus(w *Workspace)                { w.Settle(true) }
func modelSettleLeft(w *Workspace)               { w.Settle(false) }
func focusNote(w *Workspace, id WindowID)        { w.FocusID(id); w.Settle(true) }
func modelScroll(w *Workspace)                   { w.SettleLayout(); w.View = w.Engine().ScrollTarget(w.Snapshot()) }
func modelAddColumnQuiet(w *Workspace, c Column) { w.AddTile(c.Windows[0], c.Cells, true) }
func modelPreviewTiles(w *Workspace) ([]Placement, int, Rect) {
	return modelPreviewTilesInto(w, make([]Placement, 0, modelColumnWindows(w)))
}
func modelPreviewTilesInto(w *Workspace, dst []Placement) ([]Placement, int, Rect) {
	rows := 0
	for _, c := range w.Columns {
		rows = max(rows, len(c.Windows))
	}
	return w.Engine().PreviewTiles(w.Snapshot(), dst, make([]Rect, 0, len(w.Columns)+rows))
}
func modelOverviewZoom(w *Workspace, axis layout.Axis) float64 {
	_, span, _ := modelPreviewTiles(w)
	if span <= 0 {
		return overviewMaxZoom
	}
	room := float64(w.OverviewArea().W) * 0.96
	if axis == layout.Vertical {
		room = float64(w.OverviewArea().W-modelPileWidth(w)) * 0.96
	}
	z := min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
	_, n := w.StackHead()
	if n > 1 {
		z = min(z, overviewMaxZoom/(1+4*overviewPeekStep))
	}
	return z
}
func modelPeekStep(w *Workspace, axis layout.Axis) int {
	return max(1, int(math.Round(float64(w.Usable.H)*modelOverviewZoom(w, axis)*overviewPeekStep)))
}
func modelPileWidth(w *Workspace) int {
	if len(w.Stash) == 0 {
		return 0
	}
	a := w.OverviewArea()
	width := 80 // default stash percentage
	// Tests using this measurement use the default stash settings.
	r := Rect{W: w.Usable.W * width / 100}
	card := int(math.Round(float64(r.W) * overviewCardZoom))
	step := min(overviewCardStep, a.W/100, a.H/50)
	return min(card+(min(len(w.Stash), overviewCards)-1)*step+a.W*2/100, a.W/3)
}
func modelHiddenColumnRects(w *Workspace) []Rect {
	v := *w
	v.DropState()
	v.Columns = slices.Delete(slices.Clone(w.Columns), w.Focus, w.Focus+1)
	v.Focus = 0
	return slices.Insert(modelColumnRectsFor(&v, true), w.Focus, Rect{})
}
func modelRowHeight(w *Workspace, axis layout.Axis) int { return w.RowSpan(axis) }
