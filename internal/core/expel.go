package core

import "slices"

// Consume or expel (ADR 016): Cmd+[ and Cmd+] move the focused tiled window
// one column left or right. Alone in its column it joins the bottom of the
// neighbor column (consume); in a stacked column it leaves for a new column
// (expel). A full fixed workspace never gets a new column: the window stacks
// into the neighbor column instead, past the edge on the neighbor monitor.
//
// A slot column's slot window is its first window: windows only join a
// column at the bottom, so the slot follows Windows[0].

// full reports whether a fixed-overflow workspace has no room for a column.
func (w *Workspace) full() bool {
	return w.Overflow == OverflowFixed && len(w.Columns) >= max(w.MaxColumns, 1)
}

// ConsumeOrExpel moves the focused window in direction dir (-1 or 1). It
// returns false when the window must leave for the neighbor monitor: a full
// workspace with the window stacked in the edge column.
func (w *Workspace) ConsumeOrExpel(dir int) bool {
	if len(w.Columns) == 0 || w.floatFocus || (dir != -1 && dir != 1) {
		return true
	}
	// A fullscreen window hides the others (fixed) or its column (scroll):
	// moving would break its way back.
	if w.fullscreen != 0 && (w.Overflow == OverflowFixed || w.fullscreenColumn(w.Focus)) {
		return true
	}
	src := w.Focus
	col := w.Columns[src]
	id := col.Windows[col.Focus]
	next := src + dir
	edge := next < 0 || next >= len(w.Columns)
	switch {
	case len(col.Windows) == 1 && edge:
		// Consume at the edge: move-column already crosses monitors.
	case len(col.Windows) == 1:
		// The column goes away, and a slot with it.
		w.stack(id, w.Columns[next].Windows[0])
	case !w.full():
		c := Column{Windows: []WindowID{id}, Width: col.Width}
		if col.Focus == 0 && col.Slot != 0 {
			// The slot and its width follow the slot window.
			c.Slot = col.Slot
			w.Columns[src].Slot, w.Columns[src].Width = 0, Width{}
		} else if col.Slot != 0 {
			c.Width = Width{}
		}
		w.RemoveWindow(id)
		w.insertColumn(src+max(dir, 0), c)
	case edge:
		return false
	default:
		w.stack(id, w.Columns[next].Windows[0])
	}
	return true
}

// stack moves tiled window id to the bottom of the column holding target
// and focuses it. Leaving a slot column's first row releases the slot.
func (w *Workspace) stack(id, target WindowID) {
	w.dropSlotOf(id)
	w.RemoveWindow(id)
	i := slices.IndexFunc(w.Columns, func(c Column) bool { return slices.Contains(c.Windows, target) })
	w.Columns[i].Windows = append(w.Columns[i].Windows, id)
	// Activate leaves a fullscreen of the target column, which would hide it.
	w.Activate(id)
}

// dropSlotOf clears the slot of the column whose slot window is id.
func (w *Workspace) dropSlotOf(id WindowID) {
	for i := range w.Columns {
		if w.Columns[i].Slot != 0 && w.Columns[i].Windows[0] == id {
			w.Columns[i].Slot = 0
		}
	}
}

// takeWindow removes the focused tiled window from its column for another
// workspace; its slot, if any, is released.
func (w *Workspace) takeWindow() (WindowID, bool) {
	id, ok := w.Focused()
	if !ok || w.floatFocus {
		return 0, false
	}
	w.dropSlotOf(id)
	w.RemoveWindow(id)
	return id, true
}

// expelTo puts a window expelled from the neighbor monitor on the side
// facing it (dir > 0: it came from the left): as a new edge column, or at
// the bottom of the edge column when the workspace is full.
func (w *Workspace) expelTo(id WindowID, dir int) {
	at := 0
	if dir < 0 {
		at = len(w.Columns)
	}
	if !w.full() || w.origin != nil {
		w.receive(Column{Windows: []WindowID{id}}, at)
		return
	}
	i := min(at, len(w.Columns)-1)
	w.Columns[i].Windows = append(w.Columns[i].Windows, id)
	w.Activate(id)
}
