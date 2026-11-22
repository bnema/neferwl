package workspace

import "github.com/bnema/neferwl/internal/domain"

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
// tab: the bar and, with gaps, domain.TabBarGap (at most the tile gap)
// between the bar and the window.
func (w *Workspace) TabInset() int {
	if w.TabBar <= 0 {
		return 0
	}
	return w.TabBar + min(w.Gap(), domain.TabBarGap)
}

// TabWindow moves the focused tile into the neighbor column in direction
// dir (-1 or 1) as its visible tab, as the tab drop does. It does nothing
// without a neighbor, on a float, or while a fullscreen window hides others.
func (w *Workspace) TabWindow(dir int) {
	if len(w.Columns) == 0 || w.OnFloat() || w.FullscreenHides() || (dir != -1 && dir != 1) {
		return
	}
	next := w.Focus + dir
	if next < 0 || next >= len(w.Columns) {
		return
	}
	id, ok := w.Focused()
	if !ok {
		return
	}
	w.applyTab(w, id, w.Columns[next].Selected())
}
