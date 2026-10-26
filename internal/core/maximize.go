package core

import "slices"

// maximize sets FullWidth on column i; unlike ToggleFullWidth it never toggles.
func (w *lanes) maximize(i int) {
	if i >= 0 && i < len(w.Columns) {
		w.Columns[i].FullWidth = true
		id := w.Columns[i].Windows[w.Columns[i].Focus]
		w.maximized = slices.DeleteFunc(w.maximized, func(v WindowID) bool { return v == id })
		w.maximized = slices.Insert(w.maximized, 0, id)
	}
}

// hiddenByMaximized reports whether the maximized focused column hides
// column i on screen: in fixed overflow it hides the rest of the row. A
// maximized cascade column has its band to itself, so it hides nothing.
func (w *layoutView) hiddenByMaximized(i int) bool {
	return w.layout.maximizeHides() && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth && i != w.Focus
}

// unmaximize clears the focused column in scroll and cascade, or all
// maximized columns in fixed overflow (including after Escape changed focus).
func (w *lanes) unmaximize() {
	if w.layout().maximizeHides() {
		w.clearMaximized()
	} else if w.Focus < len(w.Columns) {
		w.Columns[w.Focus].FullWidth = false
	}
}

// clearMaximized unmaximizes every column and forgets the history.
func (w *lanes) clearMaximized() {
	for i := range w.Columns {
		w.Columns[i].FullWidth = false
	}
	w.maximized = nil
}

// transferMaximization changes the maximized column without forgetting MRU.
func (w *lanes) transferMaximization(i int) {
	if i == w.Focus || i < 0 || i >= len(w.Columns) {
		return
	}
	w.Columns[w.Focus].FullWidth = false
	w.maximize(i)
}

// ToggleFullWidth expands the focused tiled column without changing its saved width.
func (w *Workspace) ToggleFullWidth() {
	if len(w.Columns) == 0 || w.onFloat() {
		return
	}
	w.layout().toggleFullWidth(w.edit())
	w.scroll()
}

func (w *Workspace) ToggleFullscreen() {
	id, ok := w.Focused()
	if !ok {
		return
	}
	if w.stashOverCover() {
		// A stashed window the user picked over the cover replaces it.
		w.leaveFullscreen()
	}
	if w.fullscreen == id {
		// The user was on it: it keeps the focus, in front of floats that
		// mapped meanwhile. Cleared first, so that floats it covers drop
		// below the columns.
		w.fullscreen = 0
		w.FocusID(id)
		w.endFullscreen()
		return
	}
	w.isolate(id)
	w.fullscreen = id
	w.scroll()
}

// endFullscreen ends the fullscreen: floats it kept demoted are promoted
// again and the view follows the focus.
func (w *Workspace) endFullscreen() {
	w.fullscreen, w.stashOver = 0, 0
	w.reconcileFloats()
	w.scroll()
}

// leaveFullscreen ends the fullscreen for another window the user chose,
// and refuses its client's requests until it is focused again (mayCover,
// settleLeft).
func (w *Workspace) leaveFullscreen() {
	if w.fullscreen != 0 {
		w.left = w.fullscreen
	}
	w.endFullscreen()
}

// Activate focuses a window and makes it visible: it leaves another
// window's fullscreen, which hides everything else. Over a covering
// window, a dialog drawn over it, a stashed window or a dialog of one
// shows over it instead, as ToggleStashVisible does: the cover stays
// fullscreen behind, so a game never sees its fullscreen end. A stashed
// window whose own fullscreen waited while hidden replaces the cover
// (coverStays); a dialog of a stashed window activates that window
// first.
func (w *Workspace) Activate(id WindowID) {
	if c := w.cover(); c != 0 && id != c {
		owner := id
		if f := w.floatIndex(id); f >= 0 {
			owner = w.dialogOwner(w.Floats[f], c)
		}
		if owner != id && owner != c && w.stashIndex(owner) >= 0 && !w.coverStays(owner) {
			w.Activate(owner)
			w.FocusID(id)
			w.focusModal(0)
			return
		}
		if i := w.stashIndex(owner); i >= 0 && w.coverStays(owner) {
			if !w.stashOverCover() {
				w.showStashOver()
			}
			w.stashAt, w.stashFocus, w.floatFocus = i, true, false
		}
		if owner == c || w.stashOverCover() && w.stashIndex(owner) >= 0 {
			if owner != id {
				w.FocusID(id)
			}
			w.focusModal(0)
			return
		}
	}
	if i := w.stashIndex(id); i >= 0 && w.stashHidden {
		// A hidden stashed window must be seen: show the stash with it,
		// selected. Another window's fullscreen leaves first, so its own
		// pending fullscreen comes back.
		w.leaveFullscreen()
		w.stashAt = i
		w.showStash()
	}
	if w.fullscreen != 0 && w.fullscreen != id {
		w.leaveFullscreen()
	}
	w.FocusID(id)
	w.focusModal(0)
}

// coverStays reports whether activating stashed window id keeps the
// covering window fullscreen under the stash: a cover outside the stash,
// and no pending fullscreen of id's own that would replace it.
func (w *Workspace) coverStays(id WindowID) bool {
	c := w.cover()
	return c != 0 && w.stashIndex(c) < 0 && !(w.stashHidden && w.hiddenFullscreen == id)
}

// SetFullscreen applies a client request. It never moves focus: client
// requests are automatic events (ADR 011 golden rule).
func (w *Workspace) SetFullscreen(id WindowID, on bool) {
	if on && !w.mayCover(id) {
		return
	}
	if w.isFloat(id) {
		if w.stashHidden && w.stashIndex(id) >= 0 {
			// Applied when the stash is shown again.
			if on {
				w.hiddenFullscreen = id
			} else if w.hiddenFullscreen == id {
				w.hiddenFullscreen = 0
			}
			return
		}
	}
	if !w.has(id) {
		return
	}
	if !on {
		if w.fullscreen == id {
			w.endFullscreen()
		}
		return
	}
	if id != w.stashOver {
		// Another window covers: the override was for the old cover.
		w.dropStashOver()
	}
	w.isolate(id)
	w.fullscreen = id
	if !w.isFloat(id) {
		w.scroll()
	}
}
