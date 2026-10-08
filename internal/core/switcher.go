package core

import (
	"slices"
)

// The column switcher is an Alt+Tab for the tiled columns of the workspace on
// screen. Each workspace keeps the columns it focused, most recent first
// (recent); the switcher steps through them without moving the focus, and
// commits the selected one when the command key is released.

// switcherRecentMax caps a workspace's recent list.
const switcherRecentMax = 64

// switcherState is the switcher of a monitor. A window stands for each
// column in order, the focused column first. Nothing moves until the commit;
// shown is set once the cards are drawn.
type switcherState struct {
	open, shown bool
	ws          *Workspace
	order       []WindowID
	at          int
	// sizes are the client sizes the cards are scaled from.
	sizes map[WindowID]Rect
}

// previewing reports whether the monitor draws previews instead of its
// workspace: the overview, or the switcher's cards.
func (m *Monitor) previewing() bool { return m.ov.open || m.sw.shown }

// noteFocus records the focused column as the most recently used. It does
// nothing while a float or the stash has the focus, and allocates nothing
// when the focused column is already the latest.
func (w *Workspace) noteFocus() {
	if w.floatFocus || w.stashFocused() || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return
	}
	col := w.Columns[w.Focus]
	id := col.Windows[col.Focus]
	if len(w.recent) > 0 && w.columnOf(w.recent[0]) == w.Focus {
		w.recent[0] = id
		return
	}
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return v == id || w.columnOf(v) == w.Focus })
	w.recent = slices.Insert(w.recent, 0, id)
	if len(w.recent) > switcherRecentMax {
		w.recent = w.recent[:switcherRecentMax]
	}
}

// switchOrder lists one window per column, the focused window of each: the
// focused column first, then the others by recent use, then those never
// used by position. It is nil with fewer than two columns.
func (w *Workspace) switchOrder() []WindowID {
	n := len(w.Columns)
	if n < 2 || w.Focus < 0 || w.Focus >= n {
		return nil
	}
	seen := make([]bool, n)
	order := make([]WindowID, 0, n)
	add := func(i int) {
		if seen[i] {
			return
		}
		seen[i] = true
		c := w.Columns[i]
		order = append(order, c.Windows[c.Focus])
	}
	add(w.Focus)
	for _, id := range w.recent {
		if i := w.columnOf(id); i >= 0 {
			add(i)
		}
	}
	for i := range n {
		add(i)
	}
	return order
}
