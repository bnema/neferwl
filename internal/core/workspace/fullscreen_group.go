package workspace

import "slices"

// The fullscreen group: w.Fullscreen is the active cover, Lanes.Held the
// other windows that stay fullscreen without covering. Invariants: the
// active cover is never held, a window is held at most once, and every held
// window is on the workspace.

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

// dropHeld removes id from the held windows, keeping the backing slice.
func (w *Workspace) dropHeld(id WindowID) {
	if len(w.Held) == 0 {
		return
	}
	w.Held = slices.DeleteFunc(w.Held, func(v WindowID) bool { return v == id })
}

// IsHeldFloat reports whether float id is held: held floats are not shown.
func (w *Workspace) IsHeldFloat(id WindowID) bool {
	return len(w.Held) > 0 && slices.Contains(w.Held, id)
}
