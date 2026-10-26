package layout

import "slices"

// Maximize sets FullWidth on column i; unlike ToggleFullWidth it never toggles.
func (w *Lanes) Maximize(i int) {
	if i >= 0 && i < len(w.Columns) {
		w.Columns[i].FullWidth = true
		id := w.Columns[i].Windows[w.Columns[i].Focus]
		w.Maximized = slices.DeleteFunc(w.Maximized, func(v WindowID) bool { return v == id })
		w.Maximized = slices.Insert(w.Maximized, 0, id)
	}
}

// HiddenByMaximized reports whether the maximized focused column hides
// column i on screen: in fixed overflow it hides the rest of the row. A
// maximized cascade column has its band to itself, so it hides nothing.
func (w *View) HiddenByMaximized(i int) bool {
	return w.Layout.MaximizeHides() && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth && i != w.Focus
}

// Unmaximize clears the focused column in scroll and cascade, or all
// maximized columns in fixed overflow (including after Escape changed focus).
func (w *Lanes) Unmaximize() {
	if w.Engine().MaximizeHides() {
		w.ClearMaximized()
	} else if w.Focus < len(w.Columns) {
		w.Columns[w.Focus].FullWidth = false
	}
}

// ClearMaximized unmaximizes every column and forgets the history.
func (w *Lanes) ClearMaximized() {
	for i := range w.Columns {
		w.Columns[i].FullWidth = false
	}
	w.Maximized = nil
}

// TransferMaximization changes the maximized column without forgetting MRU.
func (w *Lanes) TransferMaximization(i int) {
	if i == w.Focus || i < 0 || i >= len(w.Columns) {
		return
	}
	w.Columns[w.Focus].FullWidth = false
	w.Maximize(i)
}
