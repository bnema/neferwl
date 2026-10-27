package core

// ToggleFullWidth expands the focused tiled column without changing its saved width.
func (w *Workspace) ToggleFullWidth() {
	if len(w.Columns) == 0 || w.OnFloat() {
		return
	}
	w.Engine().ToggleFullWidth(w.edit())
	w.scroll()
}

// ToggleFullscreen toggles fullscreen on the focused window.
func (w *Workspace) ToggleFullscreen() {
	id, ok := w.Focused()
	if !ok {
		return
	}
	if w.stashOverCover() {
		// A stashed window the user picked over the cover replaces it.
		w.leaveFullscreen()
	}
	if w.Fullscreen == id {
		// The user was on it: it keeps the focus, in front of floats that
		// mapped meanwhile. Cleared first, so that floats it covers drop
		// below the columns.
		w.Fullscreen = 0
		w.FocusID(id)
		w.endFullscreen()
		return
	}
	w.isolate(id)
	w.Fullscreen = id
	w.scroll()
}

// endFullscreen ends the fullscreen: floats it kept demoted are promoted
// again and the view follows the focus.
func (w *Workspace) endFullscreen() {
	w.Fullscreen, w.stashOver = 0, 0
	w.reconcileFloats()
	w.scroll()
}

// leaveFullscreen ends the fullscreen for another window the user chose,
// and refuses its client's requests until it is focused again (mayCover,
// settleLeft).
func (w *Workspace) leaveFullscreen() {
	if w.Fullscreen != 0 {
		w.left = w.Fullscreen
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
	if c := w.Cover(); c != 0 && id != c {
		owner := id
		if f := w.floatIndex(id); f >= 0 {
			owner = w.dialogOwner(w.Floats[f], c)
		}
		if owner != id && owner != c && w.StashIndex(owner) >= 0 && !w.coverStays(owner) {
			w.Activate(owner)
			w.FocusID(id)
			w.FocusModal(0)
			return
		}
		if i := w.StashIndex(owner); i >= 0 && w.coverStays(owner) {
			if !w.stashOverCover() {
				w.showStashOver()
			}
			w.stashAt, w.stashFocus, w.floatFocus = i, true, false
		}
		if owner == c || w.stashOverCover() && w.StashIndex(owner) >= 0 {
			if owner != id {
				w.FocusID(id)
			}
			w.FocusModal(0)
			return
		}
	}
	if i := w.StashIndex(id); i >= 0 && w.stashHidden {
		// A hidden stashed window must be seen: show the stash with it,
		// selected. Another window's fullscreen leaves first, so its own
		// pending fullscreen comes back.
		w.leaveFullscreen()
		w.stashAt = i
		w.showStash()
	}
	if w.Fullscreen != 0 && w.Fullscreen != id {
		w.leaveFullscreen()
	}
	w.FocusID(id)
	w.FocusModal(0)
}

// coverStays reports whether activating stashed window id keeps the
// covering window fullscreen under the stash: a cover outside the stash,
// and no pending fullscreen of id's own that would replace it.
func (w *Workspace) coverStays(id WindowID) bool {
	c := w.Cover()
	return c != 0 && w.StashIndex(c) < 0 && !(w.stashHidden && w.hiddenFullscreen == id)
}

// SetFullscreen applies a client request. It never moves focus: client
// requests are automatic events (ADR 011 golden rule).
func (w *Workspace) SetFullscreen(id WindowID, on bool) {
	if on && !w.mayCover(id) {
		return
	}
	if isWorkspaceFloat(w, id) {
		if w.stashHidden && w.StashIndex(id) >= 0 {
			// Applied when the stash is shown again.
			if on {
				w.hiddenFullscreen = id
			} else if w.hiddenFullscreen == id {
				w.hiddenFullscreen = 0
			}
			return
		}
	}
	if !w.Has(id) {
		return
	}
	if !on {
		if w.Fullscreen == id {
			w.endFullscreen()
		}
		return
	}
	if id != w.stashOver {
		// Another window covers: the override was for the old cover.
		w.dropStashOver()
	}
	w.isolate(id)
	w.Fullscreen = id
	if !isWorkspaceFloat(w, id) {
		w.scroll()
	}
}
