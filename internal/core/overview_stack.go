package core

// stackItems lists the column group (0) and covering native floats in
// their actual bottom-to-top layout order. Dialogs and fullscreen are separate.
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
				w.selectOverviewColumn(w.Focus)
			}
			return
		}
	}
}

// selectOverviewColumn navigates without raising the columns or changing
// the real float order. The selection is committed by closeOverview.
func (w *Workspace) selectOverviewColumn(i int) {
	w.floatFocus, w.stashFocus = false, false
	if w.Overflow == OverflowFixed && w.Focus != i && w.Focus < len(w.Columns) {
		w.Columns[w.Focus].FullWidth = false
	}
	w.Focus = i
	w.scroll()
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
		if id == 0 {
			tiles = w.previewRow(y, dim || k > 0, lit && k == 0)
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
		if id == 0 {
			for _, c := range w.Columns {
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
