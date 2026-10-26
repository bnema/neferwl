package core

import "slices"

// FocusID selects a window and scrolls its column into view; a native
// float is raised, a stashed window is selected.
func (w *Workspace) FocusID(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 {
		f := w.Floats[i]
		f.below = false
		w.Floats = append(slices.Delete(w.Floats, i, i+1), f)
		if !w.overCover(f) {
			// A dialog shown over the cover keeps the stash over it;
			// leaving it returns there.
			w.dropStashOver()
		}
		w.floatFocus = true
		return true
	}
	if i := w.stashIndex(id); i >= 0 {
		if w.stashHidden {
			return false
		}
		w.stashAt, w.stashFocus, w.floatFocus = i, true, false
		return true
	}
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v == id {
				w.dropStashOver()
				w.floatFocus, w.stashFocus = false, false
				w.raiseColumns()
				if w.Engine().MaximizeHides() && w.Focus != i && w.Focus < len(w.Columns) && !w.Columns[i].FullWidth {
					w.Unmaximize()
				}
				w.Focus = i
				w.Columns[i].Focus = j
				w.scroll()
				return true
			}
		}
	}
	return false
}

// FocusColumn moves focus to the column on that side. From a covering
// fullscreen window it leaves fullscreen for the window there (leaveCover);
// false means it stays, with no window on that side.
func (w *Workspace) FocusColumn(dir int) bool {
	from, _ := w.Focused()
	defer w.focusModal(from)
	if w.coverDialogFocused() {
		// The first move goes back to the window under it: the stash
		// over the cover, or the cover.
		w.floatFocus = false
		return true
	}
	if w.pinned() && !w.stashOverCover() {
		return w.leaveCover(func() { w.focusColumn(dir) })
	}
	w.focusColumn(dir)
	return true
}

func (w *Workspace) focusColumn(dir int) {
	if w.floatFocus {
		// The first move leaves the native float for the stash or columns,
		// if there is one: otherwise the float keeps the focus.
		w.leaveFloat()
		return
	}
	if w.stashFocused() {
		// Inside the stash, stopping at its ends: only hiding it leaves.
		if (dir == -1 || dir == 1) && w.stashAt+dir >= 0 && w.stashAt+dir < len(w.Stash) {
			w.stashAt += dir
		}
		return
	}
	if i := w.columnToward(dir); i >= 0 {
		w.raiseColumns()
		if w.Engine().MaximizeHides() {
			w.Unmaximize()
		}
		w.Focus = i
		w.scroll()
	}
}

// columnToward returns the column focus-column-left/right (dir -1/1) goes
// to, or -1 at the edge. Scroll overflow follows the column order; fixed
// overflow stacks columns (spiral), so it follows the screen: the column
// on that side. Cascade stays in its band: past the band edge is the edge.
func (w *Workspace) columnToward(dir int) int {
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) || w.pinned() {
		return -1
	}
	return w.Engine().FocusStep(w.Snapshot(), dir)
}

// onScreenFocus reports whether directional focus follows the screen:
// fixed overflow with every column visible.
func (w *Workspace) onScreenFocus() bool {
	return w.Engine().Spiral() && screenFocus(w.Snapshot())
}

// FocusWindow moves focus inside the column, then to a fixed-overflow column
// above or below on screen. Up at the top raises the topmost demoted covering
// float; false means no window is available in that direction. From a
// covering fullscreen window it leaves fullscreen for that window.
func (w *Workspace) FocusWindow(dir int) bool {
	from, _ := w.Focused()
	defer w.focusModal(from)
	if w.coverDialogFocused() {
		w.floatFocus = false
		return true
	}
	if w.pinned() && !w.stashOverCover() {
		return w.leaveCover(func() { w.focusWindow(dir) })
	}
	return w.focusWindow(dir)
}

func (w *Workspace) focusWindow(dir int) bool {
	if w.floatFocus {
		return w.leaveFloat()
	}
	if w.stashFocused() {
		// The stash is one row: up and down do nothing there.
		return true
	}
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) {
		return false
	}
	c := &w.Columns[w.Focus]
	if c.Focus+dir >= 0 && c.Focus+dir < len(c.Windows) {
		c.Focus += dir
		w.raiseColumns()
		w.scroll()
		return true
	}
	if i := w.Engine().LaneStep(w.Snapshot(), dir); i >= 0 {
		w.Focus = i
		w.raiseColumns()
		w.Columns[i].Focus = 0
		if dir < 0 {
			w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
		}
		if !w.onScreenFocus() {
			w.scroll()
		}
		return true
	}
	if dir < 0 {
		for i := len(w.Floats) - 1; i >= 0; i-- {
			if f := w.Floats[i]; f.below && w.coversFloat(f) {
				return w.FocusID(f.ID)
			}
		}
	}
	return false
}

func abs(n int) int { return max(n, -n) }
