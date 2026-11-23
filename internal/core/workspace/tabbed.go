package workspace

import "github.com/bnema/neferwl/internal/core/layout"

// ToggleTabbed shows the focused column as tabs, or back as a stack. It does
// nothing on a float, a stash window or a one-window column. Both ways the
// rows lose their set heights: tabs have none, and the stack comes back as an
// equal split.
func (w *Workspace) ToggleTabbed() {
	if len(w.Columns) == 0 || w.OnFloat() {
		return
	}
	c := &w.Columns[w.Focus]
	if !c.Tabbed && len(c.Windows) < 2 {
		return
	}
	c.Tabbed = !c.Tabbed
	c.Shares = nil
}

// TabInset is what a tabbed column's bar takes from the top of its visible
// tab: the bar, flush with the window.
func (w *Workspace) TabInset() int {
	return max(w.TabBar, 0)
}

// TabWindow moves the focused tile into the neighbor column in direction
// dir (-1 or 1) as its visible tab, as the tab drop does. It does nothing
// without a neighbor (cascade stays in its band), on a float, while
// fullscreen hides the focused column (a cover, or the focused column holds
// the fullscreen window), or when the target column holds the fullscreen
// window: tabbing would silently drop it.
func (w *Workspace) TabWindow(dir int) {
	if len(w.Columns) == 0 || w.OnFloat() || w.FullscreenHides() || (dir != -1 && dir != 1) {
		return
	}
	next := w.columnToward(dir)
	if next < 0 || next == w.Focus {
		return
	}
	if w.FullscreenColumn(next) {
		return
	}
	id, ok := w.Focused()
	if !ok {
		return
	}
	w.applyTab(w, id, w.Columns[next].Selected())
}

// tabColumn returns the focused column when tab actions apply to it: a
// tabbed column of two or more windows, focused as a tile, with no
// fullscreen window covering or hiding it.
func (w *Workspace) tabColumn(dir int) (*layout.Column, bool) {
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) || w.OnFloat() || w.FullscreenHides() || w.Cover() != 0 {
		return nil, false
	}
	c := &w.Columns[w.Focus]
	if !c.Tabbed || len(c.Windows) < 2 {
		return nil, false
	}
	return c, true
}

// FocusTab shows the next (dir 1) or previous (dir -1) tab of the focused
// tabbed column, wrapping at the ends. It never leaves the column. A modal
// dialog of the shown tab takes the focus, as with FocusWindow.
func (w *Workspace) FocusTab(dir int) bool {
	c, ok := w.tabColumn(dir)
	if !ok {
		return false
	}
	from, _ := w.Focused()
	defer w.FocusModal(from)
	n := len(c.Windows)
	c.Focus = (c.Focus + dir + n) % n
	w.raiseColumns()
	w.scroll()
	return true
}

// MoveTab moves the visible tab one place in the tab order, focus following
// it. Past an end it wraps: the last tab moved next becomes the first, and
// the first moved previous becomes the last. It never leaves the column.
func (w *Workspace) MoveTab(dir int) bool {
	c, ok := w.tabColumn(dir)
	if !ok {
		return false
	}
	n := len(c.Windows)
	from := c.Focus
	to := (from + dir + n) % n
	if from == 0 || to == 0 {
		w.dropSlotOf(c.Windows[0])
	}
	rotate(c.Windows, from, to)
	if layout.ValidShares(*c) {
		rotate(c.Shares, from, to)
	}
	c.Focus = to
	w.scroll()
	return true
}

// rotate moves s[from] to index to, shifting the elements between them.
func rotate[T any](s []T, from, to int) {
	v := s[from]
	if from < to {
		copy(s[from:to], s[from+1:to+1])
	} else {
		copy(s[to+1:from+1], s[to:from])
	}
	s[to] = v
}
