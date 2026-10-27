package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/domain"
)

// DropSpot is an output-local model target, without screen ownership.
type DropSpot struct {
	// Kind selects the insertion, stack or swap operation.
	Kind DropKind
	// Anchor is the hovered window, or zero for insertion at the end.
	Anchor WindowID
	// Hints are output-local preview outlines.
	Hints []Rect
}

// DropIn resolves the drop of window id at the output-local point x, y,
// hints t logical px thick.
func (w *Workspace) DropIn(id WindowID, x, y, t int) DropSpot {
	layout := w.Layout()
	for _, p := range slices.Backward(layout) {
		// Floats and the stash are over the tiles: nothing lands there.
		if p.Floating && !p.Hidden && !p.Below && p.ID != id && contains(p.Rect, x, y) {
			return DropSpot{}
		}
	}
	src, srcRow := w.ColumnOf(id), -1
	if src >= 0 {
		srcRow = slices.Index(w.Columns[src].Windows, id)
	}
	lone := src >= 0 && len(w.Columns[src].Windows) == 1
	var cols []visibleColumn
	for _, p := range layout {
		if p.Floating || p.Hidden || !p.Rect.Overlaps(w.Usable) {
			continue
		}
		ci := w.ColumnOf(p.ID)
		if ci < 0 {
			continue
		}
		if n := len(cols); n > 0 && cols[n-1].index == ci {
			cols[n-1].rect = geom.Union(cols[n-1].rect, p.Rect)
		} else {
			cols = append(cols, visibleColumn{index: ci, rect: p.Rect})
		}
	}
	if len(w.Columns) == 0 || src < 0 && len(cols) == 0 {
		if !contains(w.Usable, x, y) {
			return DropSpot{}
		}
		g := w.Gap()
		r := Rect{X: w.Usable.X + g, Y: w.Usable.Y + g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
		return DropSpot{Kind: DropInsert, Hints: outline(r, t)}
	}
	if w.scrollsRow() {
		if tgt, ok := w.gapDrop(cols, id, src, lone, x, y, t); ok {
			return tgt
		}
	}
	for _, p := range layout {
		if p.Floating || p.Hidden || !contains(p.Rect, x, y) {
			continue
		}
		ci := w.ColumnOf(p.ID)
		if ci < 0 {
			continue
		}
		r := p.Rect
		band := max(r.H/dropBandDiv, 1)
		row := slices.Index(w.Columns[ci].Windows, p.ID)
		switch {
		case y < r.Y+band:
			// Above the window just below it, or above itself: no move.
			if p.ID == id || ci == src && row == srcRow+1 {
				return DropSpot{}
			}
			return DropSpot{Kind: DropAbove, Anchor: p.ID, Hints: []Rect{{X: r.X, Y: r.Y, W: r.W, H: min(t, r.H)}}}
		case y >= r.Y+r.H-band:
			if p.ID == id || ci == src && row == srcRow-1 {
				return DropSpot{}
			}
			return DropSpot{Kind: DropBelow, Anchor: p.ID, Hints: []Rect{{X: r.X, Y: r.Y + r.H - min(t, r.H), W: r.W, H: min(t, r.H)}}}
		default:
			if ci == src {
				return DropSpot{}
			}
			for _, vc := range cols {
				if vc.index == ci {
					return DropSpot{Kind: DropSwap, Anchor: p.ID, Hints: outline(vc.rect, t)}
				}
			}
			return DropSpot{}
		}
	}
	return DropSpot{}
}

// gapDrop is the insert zone of scroll overflow: the gaps between the
// columns on screen and beside the outer ones.
func (w *Workspace) gapDrop(cols []visibleColumn, id WindowID, src int, lone bool, x, y, t int) (DropSpot, bool) {
	pad := max(w.Gap()/2, gapPad)
	for i := 0; i <= len(cols); i++ {
		var mid, at int
		var ref Rect
		switch {
		case i == 0:
			ref = cols[0].rect
			mid, at = ref.X-w.Gap()/2, cols[0].index
		case i == len(cols):
			ref = cols[i-1].rect
			mid, at = ref.X+ref.W+w.Gap()/2, cols[i-1].index+1
		default:
			a, b := cols[i-1].rect, cols[i].rect
			ref = geom.Union(a, b)
			mid, at = (a.X+a.W+b.X)/2, cols[i].index
		}
		if x < mid-pad || x >= mid+pad || y < ref.Y || y >= ref.Y+ref.H {
			continue
		}
		// A lone column next to its own place does not move.
		if lone && (at == src || at == src+1) {
			return DropSpot{}, true
		}
		var anchor WindowID
		if at < len(w.Columns) {
			anchor = w.Columns[at].Windows[0]
			if anchor == id {
				anchor = w.Columns[at].Windows[1]
			}
		}
		hint := Rect{X: mid - t/2, Y: ref.Y, W: t, H: ref.H}
		return DropSpot{Kind: DropInsert, Anchor: anchor, Hints: []Rect{hint}}, true
	}
	return DropSpot{}, false
}

// scrollBy moves the view one column toward dir, within the row; the
// focus stays. It reports whether the view moved.
func (w *Workspace) scrollBy(dir int) bool {
	g := w.Gap()
	n := len(w.Columns)
	xs, ws := w.ColumnEdges()
	maxView := max(xs[n-1]+ws[n-1]+g-(w.Usable.X+w.Usable.W), 0)
	view := w.View
	if dir < 0 {
		// The first column not fully on screen at the left.
		for i := n - 1; i >= 0; i-- {
			if xs[i]-view < w.Usable.X+g {
				view = xs[i] - w.Usable.X - g
				break
			}
		}
	} else {
		for i := range n {
			if xs[i]+ws[i]-view > w.Usable.X+w.Usable.W-g {
				view = xs[i] + ws[i] + g - w.Usable.X - w.Usable.W
				break
			}
		}
	}
	view = min(max(view, 0), maxView)
	if view == w.View {
		return false
	}
	w.StopColumns()
	w.View = view
	return true
}

// makeFree turns native float f into a free float where it is now.
func (w *Workspace) makeFree(f *Float) {
	if f.free {
		return
	}
	r := w.floatRect(*f)
	if f.W <= 0 || f.H <= 0 {
		f.W, f.H = max(r.W-2*w.border, 1), max(r.H-2*w.border, 1)
	}
	f.free = true
	w.moveFree(f, r, 0, 0)
}

// visibleColumn is a column on screen and the bounds of its windows.
type visibleColumn struct {
	index int
	rect  Rect
}

// resized is r with the edges moved by dx, dy, at least min wide and
// high, inside area u.
func resized(r Rect, edges domain.ResizeEdges, dx, dy, least int, u Rect) Rect {
	left, top, right, bottom := r.X, r.Y, r.X+r.W, r.Y+r.H
	if edges&domain.ResizeLeft != 0 {
		left = min(max(left+dx, u.X), right-least)
	}
	if edges&domain.ResizeRight != 0 {
		right = max(min(right+dx, u.X+u.W), left+least)
	}
	if edges&domain.ResizeTop != 0 {
		top = min(max(top+dy, u.Y), bottom-least)
	}
	if edges&domain.ResizeBottom != 0 {
		bottom = max(min(bottom+dy, u.Y+u.H), top+least)
	}
	return Rect{X: left, Y: top, W: right - left, H: bottom - top}
}

// contains reports whether r holds the point x, y.
func contains(r Rect, x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// outline is the four sides of r, t thick, inside it.
func outline(r Rect, t int) []Rect {
	t = min(t, r.W/2, r.H/2)
	if t <= 0 {
		return nil
	}
	return []Rect{
		{X: r.X, Y: r.Y, W: r.W, H: t},
		{X: r.X, Y: r.Y + r.H - t, W: r.W, H: t},
		{X: r.X, Y: r.Y + t, W: t, H: r.H - 2*t},
		{X: r.X + r.W - t, Y: r.Y + t, W: t, H: r.H - 2*t},
	}
}

// DropKind describes how a dragged tile joins its destination.
type DropKind int

const (
	// DropNone leaves the tile in place.
	DropNone DropKind = iota
	// DropAbove stacks before the anchor window.
	DropAbove
	// DropBelow stacks after the anchor window.
	DropBelow
	// DropSwap exchanges columns or inserts beside the anchor.
	DropSwap
	// DropInsert inserts a column before the anchor, or at the end.
	DropInsert
)
