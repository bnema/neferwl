package layout

import "slices"

// FullscreenHides reports whether fullscreen hides the selected column.
func (w *Lanes) FullscreenHides() bool {
	return w.Fullscreen != 0 && (w.Engine().Covering(w.Snapshot()) || w.FullscreenColumn(w.Focus))
}

// fullscreenColumn reports whether column c holds the fullscreen window.
func fullscreenColumn(c Column, fullscreen WindowID) bool {
	return fullscreen != 0 && slices.Contains(c.Windows, fullscreen)
}

// fullscreenSized reports whether column i is output-sized: it holds the
// active cover or a held fullscreen window. Only the cover ever covers.
func (w *View) fullscreenSized(i int) bool {
	return fullscreenSizedColumn(w.Columns[i], w.Fullscreen, w.Held)
}

// fullscreenSizedColumn reports whether c holds the cover or a held window.
func fullscreenSizedColumn(c Column, fullscreen WindowID, held []WindowID) bool {
	if fullscreenColumn(c, fullscreen) {
		return true
	}
	if len(held) == 0 {
		return false
	}
	for _, id := range c.Windows {
		if slices.Contains(held, id) {
			return true
		}
	}
	return false
}
