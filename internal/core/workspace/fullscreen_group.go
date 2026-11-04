package workspace

import "slices"

// The fullscreen group: w.Fullscreen is the active cover, Lanes.Held the
// other windows that stay fullscreen without covering. Invariants: the
// active cover is never held, a window is held at most once, and every held
// window is on the workspace. Held may be non-empty with Fullscreen 0: in
// a pinned layout (cascade), focus left the group for an unrelated window
// and every member is held (leaveCover); the group comes back when focus
// lands on a member (promoteFocused). In scroll the cover stays set while
// scrolled off, so Cover() is 0 there with Fullscreen non-zero.

// IsFullscreenMember reports whether id is the active cover or held.
func (w *Workspace) IsFullscreenMember(id WindowID) bool {
	return id != 0 && (id == w.Fullscreen || slices.Contains(w.Held, id))
}

// hold keeps id fullscreen in the group without making it the cover. It
// ignores zero, the active cover, a window already held and one not on the
// workspace, and refuses under a layout that cannot scroll (fixed): with
// nowhere off screen to sit, its cover overlays everything instead.
func (w *Workspace) hold(id WindowID) {
	if id == 0 || !w.Engine().Scrolls() || id == w.Fullscreen || slices.Contains(w.Held, id) || !w.Has(id) {
		return
	}
	w.Held = append(w.Held, id)
}

// promoteHeld makes held window id the active cover; the previous cover, if
// any, is held in its place. It is not a user leave, so it never sets left.
func (w *Workspace) promoteHeld(id WindowID) {
	i := slices.Index(w.Held, id)
	if i < 0 {
		return
	}
	if prev := w.Fullscreen; prev != 0 {
		w.Held[i] = prev
	} else {
		w.Held = slices.Delete(w.Held, i, i+1)
	}
	w.Fullscreen = id
}

// promoteFocused promotes the focused window when it is held: focus on a
// held member makes it the cover. Workspace-local, it moves no seat focus.
func (w *Workspace) promoteFocused() {
	if len(w.Held) == 0 {
		return
	}
	// The selected tile, not Focused: that reports the cover over it.
	if w.floatFocus || w.stashFocused() || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return
	}
	c := w.Columns[w.Focus]
	if c.Focus >= 0 && c.Focus < len(c.Windows) && w.IsHeld(c.Selected()) {
		w.promoteHeld(c.Selected())
	}
}

// demoteCover holds the cover of a pinned layout (cascade) whose group the
// focus leaves for an unrelated window, and reports whether it did. Unlike
// leaveFullscreen it sets no left, so focus on any member brings the group
// back with no fullscreen key. A lone cover, a floating one (it cannot be
// held: held floats are not shown) and a scrolling layout's are left alone.
// The focus must already be off the group: scroll promotes a focused member.
func (w *Workspace) demoteCover() bool {
	if !w.demotable() {
		return false
	}
	full := w.Fullscreen
	w.Fullscreen = 0
	w.hold(full)
	w.stashOver = 0
	w.reconcileFloats()
	w.scroll()
	return true
}

// demotable reports whether demoteCover would hold the cover.
func (w *Workspace) demotable() bool {
	full := w.Fullscreen
	return full != 0 && len(w.Held) > 0 && !w.IsFloat(full) && w.Engine().PinsFullscreen()
}

// dropHeld removes id from the held windows, keeping the backing slice.
func (w *Workspace) dropHeld(id WindowID) {
	if len(w.Held) == 0 {
		return
	}
	w.Held = slices.DeleteFunc(w.Held, func(v WindowID) bool { return v == id })
}

// IsHeld reports whether id is a held fullscreen member (not the cover).
func (w *Workspace) IsHeld(id WindowID) bool {
	return id != 0 && len(w.Held) > 0 && slices.Contains(w.Held, id)
}
