package workspace

// Free floats: a tile the user floated with toggle-floating (or dragged
// out, core/drag.go) floats over the columns at a size and centre core owns.
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
	if !ok || id == w.Fullscreen || w.StashIndex(id) >= 0 {
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
	if i := w.floatIndex(id); i >= 0 && w.Floats[i].free && id != w.Fullscreen {
		return i
	}
	return -1
}

// resizeFree resizes a free float by a share of the usable area.
func (w *Workspace) resizeFree(i int, width bool, step int) {
	f := &w.Floats[i]
	u := w.Usable
	if u.W <= 0 || u.H <= 0 {
		return
	}
	// The size is the client's: the border must fit too. A width
	// step is in cells, a cell being Usable.W/MaxColumns.
	if width {
		f.W = max(min(f.W+step*u.W/max(w.MaxColumns, 1), u.W-2*w.border), freeMinSize)
	} else {
		f.H = max(min(f.H+u.H*step/100, u.H-2*w.border), freeMinSize)
	}
	w.clampFree(f)
}

// nudgeFree moves a free float by logical pixels inside the usable area.
func (w *Workspace) nudgeFree(i, dx, dy int) {
	f := &w.Floats[i]
	u := w.Usable
	if u.W <= 0 || u.H <= 0 {
		return
	}
	w.moveFree(f, w.floatRect(*f), dx, dy)
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
