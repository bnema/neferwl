package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// OverviewMaximized reports a fixed row whose focused column hides the
// others: maximized, or holding the covering fullscreen tile. Its card
// shows in front of the hidden columns' card.
func (w *Workspace) OverviewMaximized() bool {
	if !w.Engine().MaximizeHides() || len(w.Columns) < 2 || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return false
	}
	if c := w.Cover(); c != 0 && !isWorkspaceFloat(w, c) {
		return w.ColumnOf(c) == w.Focus
	}
	return w.Columns[w.Focus].FullWidth
}

// Stack lists what is on screen followed by what it hides, top to bottom.
// A pinned fullscreen window is the front card, a float on its own, a
// fixed tile with its column, so every window it hides stays reachable; a
// stashed one is its card in the pile.
func (w *Workspace) Stack() []StackItem {
	var items []StackItem
	w.walkStack(func(item StackItem) bool {
		items = append(items, item)
		return true
	})
	return items
}

// stacked reports whether the stack has several cards, without building it:
// the render path asks for every workspace of every frame.
func (w *Workspace) stacked() bool {
	_, n := w.StackHead()
	return n > 1
}

// StackHead is the first item of the stack and the number of items, counted
// up to 2, without building the stack.
func (w *Workspace) StackHead() (first StackItem, n int) {
	w.walkStack(func(item StackItem) bool {
		if n == 0 {
			first = item
		}
		n++
		return n < 2
	})
	return first, n
}

// walkStack yields the items of stack in order until yield returns false.
func (w *Workspace) walkStack(yield func(StackItem) bool) {
	c, columns := WindowID(0), len(w.Columns) > 0
	if w.Pinned() {
		c = w.Cover()
	}
	switch {
	case c == 0, w.StashIndex(c) >= 0:
	case w.floatIndex(c) >= 0:
		if !yield(StackItem{StackFloat, c}) {
			return
		}
	case w.OverviewMaximized():
		if !yield(StackItem{StackColumn, c}) {
			return
		}
	default:
		// One card of all columns, its own marked fullscreen.
		if !yield(StackItem{Kind: StackColumns}) {
			return
		}
		columns = false
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if !f.below && w.coversFloat(f) && !yield(StackItem{StackFloat, f.ID}) {
			return
		}
	}
	if (c == 0 || isWorkspaceFloat(w, c)) && w.OverviewMaximized() && !yield(StackItem{StackColumn, w.Columns[w.Focus].Windows[0]}) {
		return
	}
	if columns && !yield(StackItem{Kind: StackColumns}) {
		return
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		f := w.Floats[i]
		if f.below && w.coversFloat(f) && !yield(StackItem{StackFloat, f.ID}) {
			return
		}
	}
}

// ItemOf resolves a clicked tile to the card currently containing it.
func (w *Workspace) ItemOf(id WindowID) (StackItem, bool) {
	for _, item := range w.Stack() {
		switch item.Kind {
		case StackFloat:
			if item.ID == id {
				return item, true
			}
		case StackColumn:
			if i := w.ColumnOf(id); i >= 0 && i == w.ColumnOf(item.ID) {
				return item, true
			}
		case StackColumns:
			if i := w.ColumnOf(id); i >= 0 && (!w.OverviewMaximized() || i != w.Focus) {
				return item, true
			}
		}
	}
	return StackItem{}, false
}

// SelectOverviewColumn retains the existing ordinary-row live focus semantics.
func (w *Workspace) SelectOverviewColumn(i int) {
	w.floatFocus, w.stashFocus = false, false
	if w.Engine().MaximizeHides() && w.Focus != i && w.Focus < len(w.Columns) {
		w.Unmaximize()
	}
	w.Focus = i
	w.scroll()
}

// ApplyStack commits the selected front card without changing other stack items.
func (w *Workspace) ApplyStack(item StackItem, selected WindowID) {
	// Picking a card is a user focus: a blocked parent gives it to its
	// modal dialog.
	defer w.FocusModal(0)
	switch item.Kind {
	case StackFloat:
		w.FocusID(item.ID)
	case StackColumns:
		i := w.ColumnOf(selected)
		if w.OverviewMaximized() && i == w.Focus {
			i = -1
		}
		if i < 0 && len(w.Columns) > 0 {
			i = w.Focus
			if w.OverviewMaximized() {
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
			if w.OverviewMaximized() && i != w.Focus {
				w.TransferMaximization(i)
			}
			w.FocusID(selected)
		}
	case StackColumn:
		i := w.ColumnOf(item.ID)
		if i < 0 {
			return
		}
		if w.ColumnOf(selected) != i {
			c := w.Columns[i]
			selected = c.Windows[c.Focus]
		}
		if w.OverviewMaximized() && i != w.Focus {
			w.TransferMaximization(i)
		}
		w.FocusID(selected)
	}
}

// hiddenColumnRects lays out the columns behind the maximized one with the
// fixed layout rules, without the maximized column, so no hole is left
// where it was. The maximized column's own entry is an empty Rect.
func (w *Workspace) hiddenColumnRects() []Rect {
	v := *w
	// The copied state is positional and would not match the shifted columns;
	// state() rebuilds it.
	v.DropState()
	v.Columns = slices.Delete(slices.Clone(w.Columns), w.Focus, w.Focus+1)
	v.Focus = 0
	return slices.Insert(v.columnRectsFor(true), w.Focus, Rect{})
}

// StackKind identifies the window group represented by an overview card.
type StackKind uint8

const (
	// StackFloat represents one covering native float.
	StackFloat StackKind = iota
	// StackColumns represents the ordinary tiled column group.
	StackColumns
	// StackColumn represents one maximized or fullscreen column.
	StackColumn
)

// A StackItem identifies a covering float, a column group (all columns, or
// all except the maximized column), or the maximized column by one of its
// windows. Only stackColumns has a zero ID.
type StackItem struct {
	// Kind selects the represented window group.
	Kind StackKind
	// ID anchors a float or column; the ordinary column group uses zero.
	ID WindowID
}

// previewItem uses the monitor's provisional hidden-column selection as a value.
func (w *Workspace) previewItem(item StackItem, selected int, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	if item.Kind == StackColumns {
		if !w.OverviewMaximized() {
			return w.previewRow(axis, slot, dim, lit)
		}
		// The hidden columns fill the screen as if the maximized column,
		// shown on its own card, were not there. In particular, an earlier
		// maximized buffer scales into its unmaximized cell here.
		g := w.Gap()
		rects := w.hiddenColumnRects()
		var tiles []Placement
		for i, c := range w.Columns {
			if i == w.Focus {
				continue
			}
			r := rects[i]
			r.X -= w.Usable.X
			r.Y -= w.Usable.Y
			w.rowBuf = layout.RowRectsInto(w.rowBuf[:0], r, c, g)
			for j, t := range w.rowBuf {
				tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == selected && j == c.Focus})
			}
		}
		sel := rects[selected]
		sel.X -= w.Usable.X
		return w.previewRowTiles(axis, slot, dim, lit, false, tiles, w.Usable.W, sel)
	}
	g := w.Gap()
	if item.Kind == StackFloat && item.ID == w.Cover() {
		// A fullscreen float shows at the size of a fullscreen column.
		r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
		return w.previewRowTiles(axis, slot, dim, lit, false, []Placement{{ID: item.ID, Rect: r, Focused: true, Fullscreen: true}}, w.Usable.W, r)
	}
	if item.Kind == StackFloat {
		f := w.Floats[w.floatIndex(item.ID)]
		r := w.floatRect(f)
		r.X -= w.Usable.X
		r.Y -= w.Usable.Y
		return w.previewRowTiles(axis, slot, dim, lit, false, []Placement{{ID: item.ID, Rect: r, Floating: true, Focused: true}}, w.Usable.W, r)
	}
	i := w.ColumnOf(item.ID)
	if i < 0 {
		return nil
	}
	c := w.Columns[i]
	r := Rect{W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
	r.X = (w.Usable.W - r.W) / 2
	r.Y = (w.Usable.H - r.H) / 2
	tiles := make([]Placement, 0, len(c.Windows))
	w.rowBuf = layout.RowRectsInto(w.rowBuf[:0], r, c, g)
	for j, t := range w.rowBuf {
		tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: j == c.Focus, Fullscreen: c.Windows[j] == w.Cover()})
	}
	return w.previewRowTiles(axis, slot, dim, lit, false, tiles, w.Usable.W, r)
}

// stackRow draws the stack using only the monitor's provisional selections.
func (w *Workspace) stackRow(front StackItem, current bool, selected int, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	first, n := w.StackHead()
	if n == 0 {
		return w.previewRow(axis, slot, dim, lit)
	}
	if n == 1 {
		return w.previewItem(first, selected, axis, slot, dim, lit)
	}
	items := w.Stack()
	at := 0
	if current {
		at = slices.Index(items, front)
	}
	cards := make([][]Placement, len(items))
	step := w.peekStep(axis)
	// along is the offset of d steps on the overview axis.
	along := func(d int) (int, int) {
		var r Rect
		axis.Offset(&r, d*step)
		return r.X, r.Y
	}
	for i, item := range items {
		cards[i] = w.previewItem(item, selected, axis, w.frontSlot(axis, slot), dim, lit)
	}

	// fan's behind side advances through the linear stack.
	return fan(cards, at, 2, 2,
		func(d int) (int, int) { return along(-d) }, along, dim, lit)
}
