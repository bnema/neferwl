package layout

// appendPreviewColumn shares the row projection without retaining scratch or
// reading workspace state. Each layout owns the column rectangle and span.
func appendPreviewColumn(dst []Placement, rowBuf []Rect, r Rect, c Column, gap int, focused bool) []Placement {
	rowBuf = RowRectsInto(rowBuf[:0], r, c, gap)
	for j, t := range rowBuf {
		dst = append(dst, Placement{ID: c.Windows[j], Rect: t, Focused: focused && j == c.Focus})
	}
	return dst
}

// Pinned fullscreen previews expose the underlying row and tag its cover.
func previewPinned(v View, dst []Placement, rowBuf []Rect) ([]Placement, int, Rect, bool) {
	if !pinnedView(v) {
		return nil, 0, Rect{}, false
	}
	cover := v.Fullscreen
	v.Fullscreen = 0
	tiles, span, sel := v.Layout.PreviewTiles(v, dst, rowBuf)
	for i := range tiles {
		tiles[i].Fullscreen = tiles[i].ID == cover
	}
	return tiles, span, sel, true
}

func pinnedView(v View) bool { return v.Fullscreen != 0 && v.Layout.PinsFullscreen() }
