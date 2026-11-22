package workspace

import "github.com/bnema/neferwl/internal/domain"

// AnchorHints appends the drop hint rects of anchor a for tile id to dst,
// t logical px thick: a line on a stack band or a gap, the outline of the
// swapped column or of an empty workspace.
func (w *Workspace) AnchorHints(dst []Rect, id WindowID, a Anchor, t int) []Rect {
	switch a.Kind {
	case AnchorEdge:
		return append(dst, a.Rect)
	case AnchorGap:
		w.anchorBuf = w.AppendLayout(w.anchorBuf[:0])
		if w.noColumns(w.visibleColumns(w.anchorBuf), id) {
			return appendOutline(dst, a.Rect, t)
		}
		return append(dst, Rect{X: a.Rect.X + a.Rect.W/2 - t/2, Y: a.Rect.Y, W: t, H: a.Rect.H})
	case AnchorWindow:
		w.anchorBuf = w.AppendLayout(w.anchorBuf[:0])
		cols := w.visibleColumns(w.anchorBuf)
		ci := w.ColumnOf(a.Window)
		for _, pl := range w.anchorBuf {
			if pl.ID != a.Window {
				continue
			}
			r := pl.Rect
			switch {
			case a.Action == domain.DropSwap:
				for _, vc := range cols {
					if vc.index == ci {
						return appendOutline(dst, vc.rect, t)
					}
				}
			case a.Side == domain.DropSideTop:
				return append(dst, Rect{X: r.X, Y: r.Y, W: r.W, H: min(t, r.H)})
			case a.Side == domain.DropSideBottom:
				return append(dst, Rect{X: r.X, Y: r.Y + r.H - min(t, r.H), W: r.W, H: min(t, r.H)})
			case a.Side == domain.DropSideLeft:
				return append(dst, Rect{X: r.X, Y: r.Y, W: min(t, r.W), H: r.H})
			case a.Side == domain.DropSideRight:
				return append(dst, Rect{X: r.X + r.W - min(t, r.W), Y: r.Y, W: min(t, r.W), H: r.H})
			}
		}
	}
	return dst
}

// appendOutline appends the four sides of r, t thick, inside it.
func appendOutline(dst []Rect, r Rect, t int) []Rect {
	t = min(t, r.W/2, r.H/2)
	if t <= 0 {
		return dst
	}
	return append(dst,
		Rect{X: r.X, Y: r.Y, W: r.W, H: t},
		Rect{X: r.X, Y: r.Y + r.H - t, W: r.W, H: t},
		Rect{X: r.X, Y: r.Y + t, W: t, H: r.H - 2*t},
		Rect{X: r.X + r.W - t, Y: r.Y + t, W: t, H: r.H - 2*t},
	)
}
