package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// overviewMaximized reports a fixed row whose focused column hides the
// others: maximized, or holding the covering fullscreen tile. Its card
// shows in front of the hidden columns' card.
func (w *Workspace) overviewMaximized() bool {
	if !w.Engine().MaximizeHides() || len(w.Columns) < 2 || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return false
	}
	if c := w.cover(); c != 0 && !w.isFloat(c) {
		return w.ColumnOf(c) == w.Focus
	}
	return w.Columns[w.Focus].FullWidth
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
			if i := w.ColumnOf(id); i >= 0 && i == w.ColumnOf(item.id) {
				return item, true
			}
		case stackColumns:
			if i := w.ColumnOf(id); i >= 0 && (!w.overviewMaximized() || i != w.Focus) {
				return item, true
			}
		}
	}
	return stackItem{}, false
}

// selectOverviewColumn retains the existing ordinary-row live focus semantics.
func (w *Workspace) selectOverviewColumn(i int) {
	w.floatFocus, w.stashFocus = false, false
	if w.Engine().MaximizeHides() && w.Focus != i && w.Focus < len(w.Columns) {
		w.Unmaximize()
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
		i := w.ColumnOf(selected)
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
				w.TransferMaximization(i)
			}
			w.FocusID(selected)
		}
	case stackColumn:
		i := w.ColumnOf(item.id)
		if i < 0 {
			return
		}
		if w.ColumnOf(selected) != i {
			c := w.Columns[i]
			selected = c.Windows[c.Focus]
		}
		if w.overviewMaximized() && i != w.Focus {
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

// previewItem uses the monitor's provisional hidden-column selection as a value.
func (w *Workspace) previewItem(item stackItem, selected int, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	if item.kind == stackColumns {
		if !w.overviewMaximized() {
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
	i := w.ColumnOf(item.id)
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
		tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: j == c.Focus, Fullscreen: c.Windows[j] == w.cover()})
	}
	return w.previewRowTiles(axis, slot, dim, lit, false, tiles, w.Usable.W, r)
}

// stackRow draws the stack using only the monitor's provisional selections.
func (w *Workspace) stackRow(front stackItem, current bool, selected int, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	first, n := w.stackHead()
	if n == 0 {
		return w.previewRow(axis, slot, dim, lit)
	}
	if n == 1 {
		return w.previewItem(first, selected, axis, slot, dim, lit)
	}
	items := w.stack()
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
