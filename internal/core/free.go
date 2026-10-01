package core

// Free floats: a tile the user floated with toggle-floating (or dragged
// out, drag.go) floats over the columns at a size and centre core owns.
// The centre is a fraction of the usable area, so the window keeps its
// relative place across stash, workspace and monitor moves.

// freeNudge is how far a keyboard move shifts a free float, logical px.
const freeNudge = 50

// freeMinSize is the smallest client size a free float is resized to.
const freeMinSize = 64

// ToggleFloating turns the focused tile into a free float at its current
// size, centred; a float (free or native) returns to the columns: a free
// one to its former place, a native one as a new column. A fullscreen or
// stashed window is left alone.
func (w *Workspace) ToggleFloating() {
	id, ok := w.Focused()
	if !ok || id == w.fullscreen || w.stashIndex(id) >= 0 {
		return
	}
	if i := w.floatIndex(id); i >= 0 {
		back := w.Floats[i].back
		w.RemoveWindow(id)
		w.restore(id, back)
		return
	}
	r, ok := w.placed(id)
	if !ok {
		return
	}
	// Its slot is released: the slot column refills without it.
	back := w.tilePlace(id)
	back.slot = 0
	w.dropSlotOf(id)
	w.RemoveWindow(id)
	w.Floats = append(w.Floats, Float{
		ID: id, W: max(r.W-2*w.border, 1), H: max(r.H-2*w.border, 1),
		free: true, cx: 0.5, cy: 0.5, back: back,
	})
	w.floatFocus, w.stashFocus = true, false
}

// placed is the rect of window id in the current layout.
func (w *Workspace) placed(id WindowID) (Rect, bool) {
	for _, p := range w.Layout() {
		if p.ID == id && !p.Hidden {
			return p.Rect, true
		}
	}
	return Rect{}, false
}

// focusedFree returns the index of the focused free float, or -1.
func (w *Workspace) focusedFree() int {
	id, ok := w.Focused()
	if !ok || !w.floatFocus {
		return -1
	}
	if i := w.floatIndex(id); i >= 0 && w.Floats[i].free && id != w.fullscreen {
		return i
	}
	return -1
}

// freeAction runs a move or resize action on the focused free float and
// reports whether it did: moves shift it by freeNudge, resizes change its
// size by a share of the usable area.
func (w *Workspace) freeAction(a Action) bool {
	i := w.focusedFree()
	if i < 0 {
		return false
	}
	f := &w.Floats[i]
	u := w.Usable
	if axis, pct, ok := ResizeArg(a); ok {
		if u.W <= 0 || u.H <= 0 {
			return true
		}
		// The size is the client's: the border must fit too.
		if axis == ResizeWidth {
			f.W = max(min(f.W+u.W*pct/100, u.W-2*w.border), freeMinSize)
		} else {
			f.H = max(min(f.H+u.H*pct/100, u.H-2*w.border), freeMinSize)
		}
		w.clampFree(f)
		return true
	}
	dx, dy := 0, 0
	switch a {
	case ActionMoveColumnLeft:
		dx = -freeNudge
	case ActionMoveColumnRight:
		dx = freeNudge
	case ActionMoveWindowUp:
		dy = -freeNudge
	case ActionMoveWindowDown:
		dy = freeNudge
	default:
		return false
	}
	if u.W <= 0 || u.H <= 0 {
		return true
	}
	w.moveFree(f, w.floatRect(*f), dx, dy)
	return true
}

// moveFree puts free float f at rect r shifted by dx, dy, kept inside
// the usable area.
func (w *Workspace) moveFree(f *Float, r Rect, dx, dy int) {
	u := w.Usable
	x := min(max(r.X+dx, u.X), u.X+u.W-r.W)
	y := min(max(r.Y+dy, u.Y), u.Y+u.H-r.H)
	f.cx = float64(x-u.X+r.W/2) / float64(u.W)
	f.cy = float64(y-u.Y+r.H/2) / float64(u.H)
}

// clampFree keeps the centre of f where its rect stays inside the area.
func (w *Workspace) clampFree(f *Float) {
	w.moveFree(f, w.floatRect(*f), 0, 0)
}
