package layout

import "slices"

// FullscreenHides reports whether fullscreen hides the selected column.
func (w *Lanes) FullscreenHides() bool {
	return w.Fullscreen != 0 && (w.Engine().PinsFullscreen() || w.FullscreenColumn(w.Focus))
}

// FullscreenColumn reports whether column i contains the fullscreen window.
func (w *View) FullscreenColumn(i int) bool {
	return fullscreenColumn(w.Columns[i], w.Fullscreen)
}

// fullscreenColumn reports whether column c holds the fullscreen window.
func fullscreenColumn(c Column, fullscreen WindowID) bool {
	return fullscreen != 0 && slices.Contains(c.Windows, fullscreen)
}
