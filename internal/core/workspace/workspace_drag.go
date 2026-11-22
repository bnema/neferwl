package workspace

import (
	"github.com/bnema/neferwl/internal/domain"
)

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
