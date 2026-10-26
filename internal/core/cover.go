package core

import "slices"

// settleLeft drops the latch once its window has the focus again, however
// it got it; core calls it after each event.
func (w *Workspace) settleLeft() {
	if id, ok := w.Focused(); ok && id == w.left {
		w.left = 0
	}
}

// mayCover reports whether a client may make id fullscreen. In fixed
// overflow it would cover the output at once and take the keyboard from
// the focused window, so only the focused window may; the request of
// another is refused and the user can still toggle-fullscreen it. A
// hidden stash's request waits for the show (showStash). A window whose
// fullscreen the user left may only once focused again, in any overflow.
func (w *Workspace) mayCover(id WindowID) bool {
	if id == w.left {
		if focused, ok := w.Focused(); !ok || focused != id {
			return false
		}
		w.left = 0
	}
	if !w.layout().pinsFullscreen() || w.stashHidden && w.stashIndex(id) >= 0 {
		return true
	}
	focused, ok := w.Focused()
	return ok && focused == id
}

// pinned reports whether the covering fullscreen window hides every other
// window: focus moves leave it (leaveCover), and the overview shows it as
// the front card of its row. Only in scroll overflow does moving to a
// tiled neighbor scroll it off and show the target instead.
func (w *Workspace) pinned() bool {
	c := w.cover()
	return c != 0 && (w.layout().pinsFullscreen() || w.isFloat(c))
}

// leaveCover runs a focus move from the covering fullscreen window as if it
// were not fullscreen. When the move reaches another window, fullscreen
// ends and that window shows, focused; otherwise (an edge) nothing changes
// and the caller goes on to the next workspace or monitor.
func (w *Workspace) leaveCover(move func()) bool {
	w.focusCover()
	full := w.fullscreen
	w.fullscreen = 0
	move()
	if id, ok := w.Focused(); ok && id != full {
		w.fullscreen = full
		w.leaveFullscreen()
		return true
	}
	w.fullscreen = full
	return false
}

// focusCover points the focus state at the covering fullscreen window,
// which Focused already reports, so a user action acts on the window on
// screen, not on one it hides.
func (w *Workspace) focusCover() {
	c := w.cover()
	if c == 0 {
		return
	}
	if d := w.coverDialog(); d != 0 {
		// Its dialog is on screen with the focus: the action is on it.
		w.FocusID(d)
		return
	}
	if w.stashOverCover() {
		// The stash shown over it has the focus.
		w.stashFocus, w.floatFocus = true, false
		return
	}
	if w.isFloat(c) {
		w.FocusID(c)
		return
	}
	w.floatFocus, w.stashFocus = false, false
	for i, col := range w.Columns {
		if j := slices.Index(col.Windows, c); j >= 0 {
			w.Focus, w.Columns[i].Focus = i, j
		}
	}
}

// cover returns the fullscreen window that covers the output, or 0. The
// fullscreen is exclusive: while it covers, no other window of the
// workspace is drawn, so the output can scan it out.
func (w *Workspace) cover() WindowID {
	if w.fullscreen == 0 {
		return 0
	}
	if w.isFloat(w.fullscreen) || w.layout().pinsFullscreen() {
		return w.fullscreen
	}
	// Scroll overflow: only while its column is aligned on the output.
	for i, c := range w.Columns {
		if slices.Contains(c.Windows, w.fullscreen) && w.columnX(i)-w.View == 0 {
			return w.fullscreen
		}
	}
	return 0
}

// fullscreenHides reports whether a fullscreen window hides the focused
// column's layout: the others (fixed) or its own column (scroll). Column
// edits would change what the user cannot see.
func (w *Workspace) fullscreenHides() bool {
	return w.fullscreen != 0 && (w.layout().pinsFullscreen() || w.fullscreenColumn(w.Focus))
}

func (w *layoutView) fullscreenColumn(i int) bool {
	return fullscreenColumn(w.Columns[i], w.Fullscreen)
}

// fullscreenColumn reports whether column c holds the fullscreen window.
func fullscreenColumn(c Column, fullscreen WindowID) bool {
	return fullscreen != 0 && slices.Contains(c.Windows, fullscreen)
}
