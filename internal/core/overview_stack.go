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

// overviewMaximized reports a fixed row whose focused column hides the
// others: maximized, or holding the covering fullscreen tile. Its card
// shows in front of the hidden columns' card.
func (w *Workspace) overviewMaximized() bool {
	if w.Overflow != OverflowFixed || len(w.Columns) < 2 || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return false
	}
	if c := w.cover(); c != 0 && !w.isFloat(c) {
		return w.columnOf(c) == w.Focus
	}
	return w.Columns[w.Focus].FullWidth
}

func (w *Workspace) columnOf(id WindowID) int {
	c, _ := w.cell(id)
	return c - 1
}

// stack lists what is on screen followed by what it hides, top to bottom.
// A pinned fullscreen window is the front card, a float on its own, a
// fixed tile with its column, so every window it hides stays reachable; a
// stashed one is its card in the pile.
func (w *Workspace) stack() []stackItem {
	var items []stackItem
	w.walkStack(func(item stackItem) bool {
		items = append(items, item)
		return true
	})
	return items
}

// stacked reports whether the stack has several cards, without building it:
// the render path asks for every workspace of every frame.
func (w *Workspace) stacked() bool {
	_, n := w.stackHead()
	return n > 1
}

// stackHead is the first item of the stack and the number of items, counted
// up to 2, without building the stack.
func (w *Workspace) stackHead() (first stackItem, n int) {
	w.walkStack(func(item stackItem) bool {
		if n == 0 {
			first = item
		}
		n++
		return n < 2
	})
	return first, n
}

// walkStack yields the items of stack in order until yield returns false.
func (w *Workspace) walkStack(yield func(stackItem) bool) {
	c, columns := WindowID(0), len(w.Columns) > 0
	if w.pinned() {
		c = w.cover()
	}
	switch {
	case c == 0, w.stashIndex(c) >= 0:
	case w.floatIndex(c) >= 0:
		if !yield(stackItem{stackFloat, c}) {
			return
		}
	case w.overviewMaximized():
		if !yield(stackItem{stackColumn, c}) {
			return
		}
	default:
		// One card of all columns, its own marked fullscreen.
		if !yield(stackItem{kind: stackColumns}) {
			return
		}
		columns = false
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if !f.below && w.coversFloat(f) && !yield(stackItem{stackFloat, f.ID}) {
			return
		}
	}
	if (c == 0 || w.isFloat(c)) && w.overviewMaximized() && !yield(stackItem{stackColumn, w.Columns[w.Focus].Windows[0]}) {
		return
	}
	if columns && !yield(stackItem{kind: stackColumns}) {
		return
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if f.below && w.coversFloat(f) && !yield(stackItem{stackFloat, f.ID}) {
			return
		}
	}
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
	m.clearCard()
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
	if i := w.columnOf(m.ov.rows[m.ov.from].focus); w == m.ov.from && i >= 0 && i != w.Focus {
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
	if w.policy().wraps && w.hiddenByMaximized(i) {
		// A card behind a maximized cascade column takes the
		// maximization; Escape gives it back (restoreRow).
		w.transferMaximization(i)
	} else if w.policy().equalCells && w.Focus != i && w.Focus < len(w.Columns) {
		w.unmaximize()
	}
	w.Focus = i
	w.scroll()
}

// apply commits the selected front card without changing other stack items.
func (w *Workspace) apply(item stackItem, selected WindowID) {
	// Picking a card is a user focus: a blocked parent gives it to its
	// modal dialog.
	defer w.focusModal(0)
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
func (m *Monitor) previewItem(w *Workspace, item stackItem, axis layoutAxis, slot Rect, dim, lit bool) []Placement {
	if item.kind == stackColumns {
		if !w.overviewMaximized() {
			return w.previewRow(axis, slot, dim, lit)
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
			w.rowBuf = rowRectsInto(w.rowBuf[:0], r, c, g)
			for j, t := range w.rowBuf {
				tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == selected && j == c.Focus})
			}
		}
		sel := rects[selected]
		sel.X -= w.Usable.X
		return w.previewRowTiles(axis, slot, dim, lit, false, tiles, w.Usable.W, sel)
	}
	g := w.gap()
	if item.kind == stackFloat && item.id == w.cover() {
		// A fullscreen float shows at the size of a fullscreen column.
		r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
		return w.previewRowTiles(axis, slot, dim, lit, false, []Placement{{ID: item.id, Rect: r, Focused: true, Fullscreen: true}}, w.Usable.W, r)
	}
	if item.kind == stackFloat {
		f := w.Floats[w.floatIndex(item.id)]
		r := w.floatRect(f)
		r.X -= w.Usable.X
		r.Y -= w.Usable.Y
		return w.previewRowTiles(axis, slot, dim, lit, false, []Placement{{ID: item.id, Rect: r, Floating: true, Focused: true}}, w.Usable.W, r)
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
	w.rowBuf = rowRectsInto(w.rowBuf[:0], r, c, g)
	for j, t := range w.rowBuf {
		tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: j == c.Focus, Fullscreen: c.Windows[j] == w.cover()})
	}
	return w.previewRowTiles(axis, slot, dim, lit, false, tiles, w.Usable.W, r)
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

// stackRow draws up to two cards on each side of the provisional front,
// fanned along axis.
func (m *Monitor) stackRow(w *Workspace, axis layoutAxis, slot Rect, dim, lit bool) []Placement {
	first, n := w.stackHead()
	if n == 0 {
		return w.previewRow(axis, slot, dim, lit)
	}
	if n == 1 {
		return m.previewItem(w, first, axis, slot, dim, lit)
	}
	items := w.stack()
	at := 0
	if w == m.Current() {
		at = slices.Index(items, m.stackFront(w))
	}
	cards := make([][]Placement, len(items))
	step := w.peekStep()
	// along is the offset of d steps on the overview axis.
	along := func(d int) (int, int) {
		var r Rect
		axis.offset(&r, d*step)
		return r.X, r.Y
	}
	for i, item := range items {
		cards[i] = m.previewItem(w, item, axis, w.frontSlot(axis, slot), dim, lit)
	}

	// fan's behind side advances through the linear stack.
	return fan(cards, at, 2, 2, false,
		func(d int) (int, int) { return along(-d) }, along, dim, lit)
}
