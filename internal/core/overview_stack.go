package core

// 0 identifies the columns (the hidden columns in a fixed maximized row).
// Window IDs cannot reach this reserved value; the maximized column is a
// second card, above the hidden group and below the covering floats.
const overviewMaxColumn WindowID = ^WindowID(0)

func (w *Workspace) overviewMaximized() bool {
	return w.Overflow == OverflowFixed && len(w.Columns) > 1 && w.Focus >= 0 && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth && !w.pinned()
}

// stackItems lists the columns (split into hidden and maximized cards in
// fixed overflow) and covering native floats in bottom-to-top layout order.
// Dialogs and fullscreen are separate.
func (w *Workspace) stackItems() []WindowID {
	if w.pinned() {
		return nil
	}
	var items []WindowID
	for _, f := range w.Floats {
		if f.below && w.coversFloat(f) {
			items = append(items, f.ID)
		}
	}
	if len(w.Columns) > 0 {
		items = append(items, 0)
		if w.overviewMaximized() {
			items = append(items, overviewMaxColumn)
		}
	}
	for _, f := range w.Floats {
		if !f.below && w.coversFloat(f) {
			items = append(items, f.ID)
		}
	}
	return items
}

// stackFront uses a provisional front only while that item still exists in
// this row. A removed float falls back to the real front.
func (m *Monitor) stackFront(w *Workspace) WindowID {
	items := w.stackItems()
	if len(items) == 0 {
		return 0
	}
	if m.overviewStackOf == w {
		for _, id := range items {
			if id == m.overviewStack {
				return id
			}
		}
	}
	return items[len(items)-1]
}

func (m *Monitor) rotateStack(w *Workspace, dir int) {
	items := w.stackItems()
	if len(items) < 2 {
		return
	}
	front := m.stackFront(w)
	for i, id := range items {
		if id == front {
			// Forward brings the card immediately behind to the front.
			m.overviewStackOf, m.overviewStack = w, items[(i-dir+len(items))%len(items)]
			if m.overviewStack == 0 {
				if w.overviewMaximized() {
					m.selectHiddenColumn(w, m.hiddenColumn(w))
				} else {
					w.selectOverviewColumn(w.Focus)
				}
			}
			return
		}
	}
}

// hiddenColumn resolves the selected window on every use. If it was removed
// or moved out of the hidden card, choose the nearest surviving column to
// its last position, excluding the maximized column.
func (m *Monitor) hiddenColumn(w *Workspace) int {
	if !w.overviewMaximized() {
		return -1
	}
	for i, c := range w.Columns {
		if i != w.Focus {
			for _, id := range c.Windows {
				if id == m.overviewHiddenID && id != 0 {
					return i
				}
			}
		}
	}
	i := m.overviewHiddenAt
	if i < 0 {
		i = w.Focus - 1
		if i < 0 {
			i = 1
		}
	}
	i = min(max(i, 0), len(w.Columns)-1)
	if i == w.Focus {
		if i > 0 {
			i--
		} else {
			i++
		}
	}
	return i
}

// selectHiddenColumn keeps a selection inside the hidden card without
// changing the maximized column, its focus, or any client's geometry.
func (m *Monitor) selectHiddenColumn(w *Workspace, i int) {
	if !w.overviewMaximized() {
		return
	}
	if i < 0 || i >= len(w.Columns) || i == w.Focus {
		i = m.hiddenColumn(w)
	}
	m.overviewHiddenAt = i
	c := w.Columns[i]
	m.overviewHiddenID = c.Windows[c.Focus]
}

// selectOverviewColumn navigates without raising columns or floats. In an
// ordinary row it changes focus live; fixed maximized rows instead use the
// provisional selectHiddenColumn until closeOverview.
func (w *Workspace) selectOverviewColumn(i int) {
	w.floatFocus, w.stashFocus = false, false
	if w.Overflow == OverflowFixed && w.Focus != i && w.Focus < len(w.Columns) {
		w.Columns[w.Focus].FullWidth = false
	}
	w.Focus = i
	w.scroll()
}

// previewColumnCard uses the screen rect for the maximized card and the
// ordinary fixed layout (omitting that column's slot) for the hidden card.
func (m *Monitor) previewColumnCard(w *Workspace, id WindowID, y int, dim, lit bool) []Placement {
	g := w.gap()
	rects := w.columnRectsFor(id == 0)
	var tiles []Placement
	for i, c := range w.Columns {
		if (i == w.Focus) != (id == overviewMaxColumn) {
			continue
		}
		r := rects[i]
		r.X -= w.Usable.X
		r.Y -= w.Usable.Y
		for j, tile := range stackRects(r, len(c.Windows), g) {
			selected := i == w.Focus && id == overviewMaxColumn || i == m.hiddenColumn(w) && id == 0
			tiles = append(tiles, Placement{ID: c.Windows[j], Rect: tile, Focused: selected && j == c.Focus})
		}
	}
	sel := rects[w.Focus]
	sel.X -= w.Usable.X
	return w.previewRowTiles(y, dim, lit, tiles, w.Usable.W, sel)
}

// stackRow draws at most two cards behind the front, then the front. The
// stash pile is placed separately on the left by overviewLayout.
func (m *Monitor) stackRow(w *Workspace, y int, dim, lit bool) []Placement {
	items := w.stackItems()
	if len(items) < 2 {
		// Without a stack, keep the existing column row (native floats
		// alone remain hidden, as before).
		return w.previewRow(y, dim, lit)
	}
	front := m.stackFront(w)
	idx := 0
	for i, id := range items {
		if id == front {
			idx = i
			break
		}
	}
	var out []Placement
	shown := min(len(items), 3)
	for k := shown - 1; k >= 0; k-- {
		id := items[(idx-k+len(items))%len(items)]
		var tiles []Placement
		if id == 0 || id == overviewMaxColumn {
			if w.overviewMaximized() {
				tiles = m.previewColumnCard(w, id, y, dim || k > 0, lit && k == 0)
			} else {
				tiles = w.previewRow(y, dim || k > 0, lit && k == 0)
			}
		} else {
			g := w.gap()
			r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
			tiles = w.previewRowTiles(y, dim || k > 0, lit && k == 0,
				[]Placement{{ID: id, Rect: r, Floating: true, Focused: true}}, r.X+r.W+g, r)
		}
		step := k * max(w.cardStep()*2, w.Usable.H/40)
		for i := range tiles {
			tiles[i].Rect.X += step
			tiles[i].Rect.Y -= step
		}
		out = append(out, tiles...)
	}
	for k := shown; k < len(items); k++ {
		id := items[(idx-k+len(items))%len(items)]
		if id == 0 || id == overviewMaxColumn {
			for i, c := range w.Columns {
				if w.overviewMaximized() && (i == w.Focus) != (id == overviewMaxColumn) {
					continue
				}
				for _, v := range c.Windows {
					out = append(out, Placement{ID: v, Hidden: true})
				}
			}
		} else {
			out = append(out, Placement{ID: id, Hidden: true})
		}
	}
	return out
}
