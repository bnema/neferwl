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

// stack preserves the current bottom-to-top layout order. The column group
// and the maximized column are separate cards in fixed overflow.
func (w *Workspace) stack() []stackItem {
	if w.pinned() {
		return nil
	}
	var items []stackItem
	for _, f := range w.Floats {
		if f.below && w.coversFloat(f) {
			items = append(items, stackItem{stackFloat, f.ID})
		}
	}
	if len(w.Columns) > 0 {
		items = append(items, stackItem{kind: stackColumns})
		if w.overviewMaximized() {
			items = append(items, stackItem{stackColumn, w.Columns[w.Focus].Windows[0]})
		}
	}
	for _, f := range w.Floats {
		if !f.below && w.coversFloat(f) {
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
	return items[len(items)-1]
}

// setFront records a provisional card and its index for removal fallback.
func (m *Monitor) setFront(w *Workspace, item stackItem) {
	m.ov.row, m.ov.front = w, item
	m.ov.frontAt = slices.Index(w.stack(), item)
}

func (m *Monitor) rotateStack(w *Workspace, dir int) {
	items := w.stack()
	if len(items) < 2 {
		return
	}
	for i, item := range items {
		if item == m.stackFront(w) {
			at := (i - dir + len(items)) % len(items)
			m.setFront(w, items[at])
			if m.ov.front.kind == stackColumns {
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

// hiddenColumn keeps the current hidden-group selection stable across column
// reordering. If its window vanished, choose the nearest surviving column.
func (m *Monitor) hiddenColumn(w *Workspace) int {
	if !w.overviewMaximized() {
		return -1
	}
	for i, c := range w.Columns {
		if i != w.Focus && slices.Contains(c.Windows, m.ov.selected) && m.ov.selected != 0 {
			return i
		}
	}
	i := m.ov.selectedAt
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

func (m *Monitor) selectHiddenColumn(w *Workspace, i int) {
	if !w.overviewMaximized() {
		return
	}
	if i < 0 || i >= len(w.Columns) || i == w.Focus {
		i = m.hiddenColumn(w)
	}
	m.ov.selectedAt = i
	c := w.Columns[i]
	m.ov.selected = c.Windows[c.Focus]
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

// apply commits a stack selection. A hidden column currently ends
// maximization through FocusID, just as it did before this refactor.
func (w *Workspace) apply(item stackItem, selected WindowID) {
	switch item.kind {
	case stackFloat:
		w.FocusID(item.id)
	case stackColumn:
		if i := w.columnOf(item.id); i >= 0 {
			if w.columnOf(selected) != i {
				selected = w.Columns[i].Windows[w.Columns[i].Focus]
			}
			w.FocusID(selected)
		}
	case stackColumns:
		if i := w.columnOf(selected); i >= 0 {
			w.FocusID(selected)
		} else if len(w.Columns) > 0 {
			c := w.Columns[w.Focus]
			w.FocusID(c.Windows[c.Focus])
		}
	}
}

// previewItem draws the card's existing geometry without changing any client.
func (m *Monitor) previewItem(w *Workspace, item stackItem, y int, dim, lit bool) []Placement {
	if item.kind == stackColumns && !w.overviewMaximized() {
		return w.previewRow(y, dim, lit)
	}
	if item.kind == stackFloat {
		g := w.gap()
		r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
		return w.previewRowTiles(y, dim, lit,
			[]Placement{{ID: item.id, Rect: r, Floating: true, Focused: true}}, r.X+r.W+g, r)
	}
	g := w.gap()
	rects := w.columnRectsFor(item.kind == stackColumns)
	var tiles []Placement
	for i, c := range w.Columns {
		if (i == w.Focus) != (item.kind == stackColumn) {
			continue
		}
		r := rects[i]
		r.X -= w.Usable.X
		r.Y -= w.Usable.Y
		for j, tile := range stackRects(r, len(c.Windows), g) {
			selected := i == w.Focus && item.kind == stackColumn || i == m.hiddenColumn(w) && item.kind == stackColumns
			tiles = append(tiles, Placement{ID: c.Windows[j], Rect: tile, Focused: selected && j == c.Focus})
		}
	}
	sel := rects[w.Focus]
	sel.X -= w.Usable.X
	return w.previewRowTiles(y, dim, lit, tiles, w.Usable.W, sel)
}

// stackRow draws two cards behind the front; stash is separate on the left.
func (m *Monitor) stackRow(w *Workspace, y int, dim, lit bool) []Placement {
	items := w.stack()
	if len(items) < 2 {
		return w.previewRow(y, dim, lit)
	}
	front := m.stackFront(w)
	idx := slices.Index(items, front)
	cards := make([][]Placement, len(items))
	for i, item := range items {
		cards[i] = m.previewItem(w, item, y, dim, lit)
	}
	step := max(w.cardStep()*2, w.Usable.H/40)
	return fan(cards, idx, 2, 0, func(distance int) (int, int) { return distance * step, -distance * step },
		func(distance int) (int, int) { return 0, 0 }, dim, lit)
}
