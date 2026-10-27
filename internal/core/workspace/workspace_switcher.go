package workspace

import (
	"slices"
)

// noteFocus records the focused column as the most recently used. It does
// nothing while a float or the stash has the focus, and allocates nothing
// when the focused column is already the latest.
func (w *Workspace) noteFocus() {
	if w.floatFocus || w.stashFocused() || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return
	}
	col := w.Columns[w.Focus]
	id := col.Windows[col.Focus]
	if len(w.recent) > 0 && slices.Contains(col.Windows, w.recent[0]) {
		w.recent[0] = id
		return
	}
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return v == id || w.ColumnOf(v) == w.Focus })
	w.recent = slices.Insert(w.recent, 0, id)
	if len(w.recent) > switcherRecentMax {
		w.recent = w.recent[:switcherRecentMax]
	}
}

// SwitchOrder lists one window per column, the focused window of each: the
// focused column first, then the others by recent use, then those never
// used by position. It is nil with fewer than two columns.
func (w *Workspace) SwitchOrder() []WindowID {
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
		if i := w.ColumnOf(id); i >= 0 {
			add(i)
		}
	}
	for i := range n {
		add(i)
	}
	return order
}

// switcherRecentMax caps a workspace's recent list.
const switcherRecentMax = 64
