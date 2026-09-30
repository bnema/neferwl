package core

import "slices"

type stackKind uint8

const (
	stackFloat stackKind = iota
	stackColumns
	stackColumn
)

// A stackItem identifies a covering float, a column group (all columns, or
// all except the maximized column), or the maximized column by one of its
// windows. Only stackColumns has a zero ID.
type stackItem struct {
	kind stackKind
	id   WindowID
}

func (w *Workspace) overviewMaximized() bool {
	return w.Overflow == OverflowFixed && len(w.Columns) > 1 && w.Focus >= 0 && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth && !w.pinned()
}

func (w *Workspace) columnOf(id WindowID) int {
	for i, c := range w.Columns {
		if slices.Contains(c.Windows, id) {
			return i
		}
	}
	return -1
}

// stack lists what is on screen followed by what it hides, top to bottom.
func (w *Workspace) stack() []stackItem {
	if w.pinned() {
		return nil
	}
	var items []stackItem
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if !f.below && w.coversFloat(f) {
			items = append(items, stackItem{stackFloat, f.ID})
		}
	}
	if w.overviewMaximized() {
		items = append(items, stackItem{stackColumn, w.Columns[w.Focus].Windows[0]})
	}
	if len(w.Columns) > 0 {
		items = append(items, stackItem{kind: stackColumns})
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if f.below && w.coversFloat(f) {
			items = append(items, stackItem{stackFloat, f.ID})
		}
	}
	return items
}

// itemOf resolves a clicked tile to the card currently containing it.
func (w *Workspace) itemOf(id WindowID) (stackItem, bool) {
	for _, item := range w.stack() {
		switch item.kind {
		case stackFloat:
			if item.id == id {
				return item, true
			}
		case stackColumn:
			if i := w.columnOf(id); i >= 0 && i == w.columnOf(item.id) {
				return item, true
			}
		case stackColumns:
			if i := w.columnOf(id); i >= 0 && (!w.overviewMaximized() || i != w.Focus) {
				return item, true
			}
		}
	}
	return stackItem{}, false
}

// stackFront resolves the provisional item against fresh workspace contents;
// an invalidated selection falls back to the nearest surviving stack index.
func (m *Monitor) stackFront(w *Workspace) stackItem {
	items := w.stack()
	if len(items) == 0 {
		return stackItem{}
	}
	if m.ov.row == w {
		for _, item := range items {
			if item == m.ov.front || item.kind == stackColumn && m.ov.front.kind == stackColumn && w.columnOf(item.id) >= 0 && w.columnOf(item.id) == w.columnOf(m.ov.front.id) {
				return item
			}
		}
		return items[min(max(m.ov.frontAt, 0), len(items)-1)]
	}
	return items[0]
}

// setFront records a provisional card and its index for removal fallback.
func (m *Monitor) setFront(w *Workspace, item stackItem) {
	m.ov.row, m.ov.front = w, item
	m.ov.frontAt = slices.Index(w.stack(), item)
}

// moveStack moves only within the current row. The caller changes workspace
// when the linear stack has no item in that direction.
func (m *Monitor) moveStack(d int) bool {
	w := m.Current()
	items := w.stack()
	if len(items) < 2 {
		return false
	}
	at := slices.Index(items, m.stackFront(w)) + d
	if at < 0 || at >= len(items) {
		return false
	}
	m.setFront(w, items[at])
	m.ov.cardOf, m.ov.card = nil, 0
	return true
}

// hiddenColumn resolves the provisional selection by window, not column
// index. With no surviving selection, prefer the most recently maximized
// hidden column, then the nearest prior non-maximized focus, then first.
func (m *Monitor) hiddenColumn(w *Workspace) int {
	if !w.overviewMaximized() {
		return -1
	}
	if i := w.columnOf(m.ov.selected); i >= 0 && i != w.Focus && m.ov.row == w {
		return i
	}
	if m.ov.row == w && m.ov.selectedAt >= 0 {
		i := min(m.ov.selectedAt, len(w.Columns)-1)
		if i == w.Focus {
			if i > 0 {
				i--
			} else {
				i++
			}
		}
		return i
	}
	for _, id := range w.maximized {
		if i := w.columnOf(id); i >= 0 && i != w.Focus {
			return i
		}
	}
	if i := w.columnOf(m.ov.fromID); w == m.ov.from && i >= 0 && i != w.Focus {
		return i
	}
	for i := range w.Columns {
		if i != w.Focus {
			return i
		}
	}
	return -1
}

// selectOverviewColumn retains the existing ordinary-row live focus semantics.
func (w *Workspace) selectOverviewColumn(i int) {
	w.floatFocus, w.stashFocus = false, false
	if w.Overflow == OverflowFixed && w.Focus != i && w.Focus < len(w.Columns) {
		w.unmaximize()
	}
	w.Focus = i
	w.scroll()
}

// apply commits the selected front card without changing other stack items.
func (w *Workspace) apply(item stackItem, selected WindowID) {
	switch item.kind {
	case stackFloat:
		w.FocusID(item.id)
	case stackColumns:
		i := w.columnOf(selected)
		if w.overviewMaximized() && i == w.Focus {
			i = -1
		}
		if i < 0 && len(w.Columns) > 0 {
			i = w.Focus
			if w.overviewMaximized() {
				for next := range w.Columns {
					if next != w.Focus {
						i = next
						break
					}
				}
			}
			c := w.Columns[i]
			selected = c.Windows[c.Focus]
		}
		if selected != 0 {
			if w.overviewMaximized() && i != w.Focus {
				w.transferMaximization(i)
			}
			w.FocusID(selected)
		}
	case stackColumn:
		i := w.columnOf(item.id)
		if i < 0 {
			return
		}
		if w.columnOf(selected) != i {
			c := w.Columns[i]
			selected = c.Windows[c.Focus]
		}
		if w.overviewMaximized() && i != w.Focus {
			w.transferMaximization(i)
		}
		w.FocusID(selected)
	}
}

// previewItem draws a group using screen geometry and a single card at its
// configured size. A prior maximized column retains its full-width buffer.
func (m *Monitor) previewItem(w *Workspace, item stackItem, y int, dim, lit bool) []Placement {
	if item.kind == stackColumns {
		if !w.overviewMaximized() {
			return w.previewRow(y, dim, lit)
		}
		// The hidden columns fill the screen as if the maximized column,
		// shown on its own card, were not there. In particular, an earlier
		// maximized buffer scales into its unmaximized cell here.
		g := w.gap()
		rects := w.hiddenColumnRects()
		var tiles []Placement
		selected := m.hiddenColumn(w)
		for i, c := range w.Columns {
			if i == w.Focus {
				continue
			}
			r := rects[i]
			r.X -= w.Usable.X
			r.Y -= w.Usable.Y
			for j, t := range stackRects(r, len(c.Windows), g) {
				tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == selected && j == c.Focus})
			}
		}
		sel := rects[selected]
		sel.X -= w.Usable.X
		return w.previewRowTiles(y, dim, lit, tiles, w.Usable.W, sel)
	}
	g := w.gap()
	if item.kind == stackFloat {
		f := w.Floats[w.floatIndex(item.id)]
		r := w.floatRect(f)
		r.X -= w.Usable.X
		r.Y -= w.Usable.Y
		return w.previewRowTiles(y, dim, lit, []Placement{{ID: item.id, Rect: r, Floating: true, Focused: true}}, w.Usable.W, r)
	}
	i := w.columnOf(item.id)
	if i < 0 {
		return nil
	}
	c := w.Columns[i]
	r := Rect{W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
	r.X = (w.Usable.W - r.W) / 2
	r.Y = (w.Usable.H - r.H) / 2
	tiles := make([]Placement, 0, len(c.Windows))
	for j, t := range stackRects(r, len(c.Windows), g) {
		tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: j == c.Focus})
	}
	return w.previewRowTiles(y, dim, lit, tiles, w.Usable.W, r)
}

// hiddenColumnRects lays out the columns behind the maximized one with the
// fixed layout rules, without the maximized column, so no hole is left
// where it was. The maximized column's own entry is an empty Rect.
func (w *Workspace) hiddenColumnRects() []Rect {
	v := *w
	v.Columns = slices.Delete(slices.Clone(w.Columns), w.Focus, w.Focus+1)
	v.Focus = 0
	return slices.Insert(v.columnRectsFor(true), w.Focus, Rect{})
}

// stackRow draws up to two cards on each side of the provisional front.
func (m *Monitor) stackRow(w *Workspace, y int, dim, lit bool) []Placement {
	items := w.stack()
	if len(items) == 0 {
		return w.previewRow(y, dim, lit)
	}
	if len(items) == 1 {
		return m.previewItem(w, items[0], y, dim, lit)
	}
	at := 0
	if w == m.Current() {
		at = slices.Index(items, m.stackFront(w))
	}
	cards := make([][]Placement, len(items))
	step := w.peekStep()
	for i, item := range items {
		cards[i] = m.previewItem(w, item, w.frontRowY(y), dim, lit)
	}

	// fan's behind side advances through the linear stack.
	return fan(cards, at, 2, 2, false,
		func(d int) (int, int) { return 0, -d * step },
		func(d int) (int, int) { return 0, d * step }, dim, lit)
}
