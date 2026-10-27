package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// AddWindow adds and focuses a tiled window with automatic width.
func (w *Workspace) AddWindow(id WindowID) { w.addWindow(id, 0) }

// addWindow is AddWindow with the width of the new column in cells (zero:
// automatic).
func (w *Workspace) addWindow(id WindowID, cells int) {
	if id == 0 || w.Has(id) {
		return
	}
	if w.Cover() != 0 {
		// A new window never moves the view off a covering fullscreen
		// window (ADR 011): it waits, hidden, after the focused column.
		at := w.Engine().CoveredSpawnAt(w.Snapshot())
		w.Columns = slices.Insert(w.Columns, at, Column{Windows: []WindowID{id}, Cells: cells})
		w.ColumnInserted(at, layout.InsertDefault)
		w.DropStaleSpans()
		return
	}
	w.addColumn(Column{Windows: []WindowID{id}, Cells: cells})
}

// addColumn places a new column like a new window and focuses it.
func (w *Workspace) addColumn(col Column) { w.placeColumn(col, false) }

// placeColumn shares placement while preserving automatic-event focus.
func (w *Workspace) placeColumn(col Column, quiet bool) {
	covered := quiet && w.Cover() != 0
	at, kind := 0, layout.InsertDefault
	if covered {
		at = w.Engine().CoveredSpawnAt(w.Snapshot())
	} else {
		at, kind = w.Engine().SpawnAt(w.Snapshot())
		if w.Engine().MaximizeHides() && len(w.Columns) > 0 {
			w.Unmaximize()
		}
		col.Span, col.SpanStamp = 0, 0
	}
	w.Columns = slices.Insert(w.Columns, at, col)
	w.ColumnInserted(at, kind)
	if !quiet && !covered {
		w.Focus = at
	} else if !covered && len(w.Columns) > 1 && at <= w.Focus {
		w.Focus++
	}
	if !covered {
		w.scroll()
	}
}

// AddSlotWindow places a slot window in its own column, ordered by slot
// number before the other columns. Focus stays on the focused window: slot
// windows are automatic events (ADR 011 golden rule).
func (w *Workspace) AddSlotWindow(id WindowID, slot int, cells int) {
	if id == 0 || w.Has(id) {
		return
	}
	// Right after the last slot column with a lower number, else first.
	at := 0
	for i, c := range w.Columns {
		if c.Slot > 0 && c.Slot < slot {
			at = i + 1
		}
	}
	w.insertAt(at, Column{Windows: []WindowID{id}, Cells: cells, Slot: slot})
	if len(w.Columns) > 1 && at <= w.Focus {
		w.Focus++
	}
	w.scroll()
}

// SetSlotCells updates the width in cells of slot column n, if present.
func (w *Workspace) SetSlotCells(n int, cells int) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Cells = cells
		}
	}
	w.scroll()
}

// InSlot reports whether window id is in slot column n.
func (w *Workspace) InSlot(id WindowID, n int) bool {
	for _, c := range w.Columns {
		if c.Slot == n && slices.Contains(c.Windows, id) {
			return true
		}
	}
	return false
}

// Unslot turns slot column n into a normal column.
func (w *Workspace) Unslot(n int) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Slot = 0
		}
	}
}

// RemoveWindow drops a window and repairs focus and fullscreen state.
func (w *Workspace) RemoveWindow(id WindowID) {
	w.Maximized = slices.DeleteFunc(w.Maximized, func(v WindowID) bool { return v == id })
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return v == id })
	if w.Fullscreen == id {
		w.Fullscreen = 0
	}
	if w.stashOver == id {
		w.stashOver = 0
	}
	if w.hiddenFullscreen == id {
		w.hiddenFullscreen = 0
	}
	if w.left == id {
		w.left = 0
	}
	if i := w.floatIndex(id); i >= 0 {
		w.Floats = slices.Delete(w.Floats, i, i+1)
		if len(w.Floats) == 0 || w.Floats[len(w.Floats)-1].below {
			w.floatFocus = false
		}
		return
	}
	if i := w.StashIndex(id); i >= 0 {
		// The right neighbor takes the selection, else the left one.
		w.Stash = slices.Delete(w.Stash, i, i+1)
		if i < w.stashAt || w.stashAt == len(w.Stash) {
			w.stashAt = max(w.stashAt-1, 0)
		}
		if len(w.Stash) == 0 {
			w.stashFocus, w.stashHidden, w.stashOver = false, false, 0
		}
		return
	}
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v != id {
				continue
			}
			c := &w.Columns[i]
			c.Windows = append(c.Windows[:j], c.Windows[j+1:]...)
			c.Shares = nil
			if len(c.Windows) == 0 {
				w.DeleteColumn(i)
				if len(w.Columns) == 0 {
					w.Focus = 0
					w.View = 0
					return
				}
				if i < w.Focus || (i == w.Focus && i > 0) {
					w.Focus--
				}
			} else {
				if j < c.Focus {
					c.Focus--
				} else if j == c.Focus && c.Focus > 0 {
					c.Focus--
				}
			}
			w.scroll()
			return
		}
	}
}

// takeColumn removes the focused column and returns it, as a normal column.
func (w *Workspace) takeColumn() (Column, bool) {
	if len(w.Columns) == 0 || w.OnFloat() {
		return Column{}, false
	}
	col := w.Columns[w.Focus]
	col.Slot = 0
	if slices.Contains(col.Windows, w.Fullscreen) {
		w.Fullscreen = 0
	}
	if slices.Contains(col.Windows, w.stashOver) {
		w.stashOver = 0
	}
	if slices.Contains(col.Windows, w.left) {
		w.left = 0
	}
	// Its windows leave this workspace's recent use with it.
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return slices.Contains(col.Windows, v) })

	col.Carry = w.DeleteColumn(w.Focus)
	w.Focus = min(w.Focus, max(len(w.Columns)-1, 0))
	if len(w.Columns) == 0 {
		w.View = 0
	} else {
		w.scroll()
	}
	return col, true
}

// insertColumn adds a column at index at (clamped) and focuses it.
func (w *Workspace) insertColumn(at int, col Column) {
	at = min(max(at, 0), len(w.Columns))
	if w.Engine().MaximizeHides() && len(w.Columns) > 0 {
		w.Unmaximize()
	}
	w.insertAt(at, col)
	w.Focus = at
	w.scroll()
}

// insertAt inserts col at index at; a laned layout lets it take the lane
// position of the column it goes before.
func (w *Workspace) insertAt(at int, col Column) {
	// Stamp identities belong to the issuing workspace and complete lane.
	col.Span, col.SpanStamp = 0, 0
	w.Columns = slices.Insert(w.Columns, at, col)
	w.ColumnInserted(at, layout.InsertJoinDisplaced)
}
