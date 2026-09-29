package core

import "slices"

type stackKind uint8

const (
	stackFloat stackKind = iota
	stackColumns
	stackColumn
)

// A stackItem identifies a covering float, all columns, or one column by a
// window it contains. Only stackColumns has a zero ID.
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
		seen := map[int]bool{w.Focus: true}
		for _, id := range w.maximized {
			i := w.columnOf(id)
			if i >= 0 && !seen[i] {
				items = append(items, stackItem{stackColumn, w.Columns[i].Windows[0]})
				seen[i] = true
			}
		}
		for i, c := range w.Columns {
			if !seen[i] {
				items = append(items, stackItem{stackColumn, c.Windows[0]})
			}
		}
	} else if len(w.Columns) > 0 {
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
			if w.columnOf(id) >= 0 {
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
	return true
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
		if w.columnOf(selected) < 0 && len(w.Columns) > 0 {
			c := w.Columns[w.Focus]
			selected = c.Windows[c.Focus]
		}
		if selected != 0 {
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
		return w.previewRow(y, dim, lit)
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
	r := w.columnRectsFor(true)[i]
	for _, id := range w.maximized {
		if w.columnOf(id) == i {
			r.W = max(w.Usable.W-2*g, 0)
			r.H = max(w.Usable.H-2*g, 0)
			break
		}
	}
	r.X = (w.Usable.W - r.W) / 2
	r.Y = (w.Usable.H - r.H) / 2
	tiles := make([]Placement, 0, len(c.Windows))
	for j, t := range stackRects(r, len(c.Windows), g) {
		tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: j == c.Focus})
	}
	return w.previewRowTiles(y, dim, lit, tiles, w.Usable.W, r)
}

// stackRow draws up to two cards on each side of the provisional front.
func (m *Monitor) stackRow(w *Workspace, y int, dim, lit bool) []Placement {
	items := w.stack()
	if len(items) < 2 {
		return w.previewRow(y, dim, lit)
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
