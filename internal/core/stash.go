package core

import (
	"cmp"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// The stash is a workspace's strip of windows set aside with
// toggle-window-stash, left to right in arrival order. The selected one
// is centred, stash.width% of the usable width and stashHeight% of its
// height; its neighbors fill the margins beside it, stash.gap% away,
// the sides, dimmed; the others wait off screen. toggle-stash-visible
// hides and shows the whole strip. Native floats (dialogs) are not in it.

// stashWidth is the default share of the usable width, in percent, a
// stashed window takes (stash.width); stashHeight is its share of the
// usable height.
const stashWidth, stashHeight = 80, 80

func (w *Workspace) stashIndex(id WindowID) int {
	return slices.IndexFunc(w.Stash, func(f Float) bool { return f.ID == id })
}

// isFloat reports whether the window floats: native or stashed.
func (w *Workspace) isFloat(id WindowID) bool {
	return w.floatIndex(id) >= 0 || w.stashIndex(id) >= 0
}

// stashFocused reports whether the stash has the focus, under any native
// float that has it. A shown stash without tiles always has it.
func (w *Workspace) stashFocused() bool {
	return (w.stashFocus || len(w.Columns) == 0) && !w.stashHidden && len(w.Stash) > 0
}

// onFloat reports whether the focus is on a floating window: column
// actions do nothing then.
func (w *Workspace) onFloat() bool { return w.floatFocus || w.stashFocused() }

// stashWindow moves the focused tile id to the end of the stash,
// remembering its place in its column to return there.
func (w *Workspace) stashWindow(id WindowID) {
	back := w.tilePlace(id)
	w.RemoveWindow(id)
	w.addStash(Float{ID: id, back: back})
}

// tilePlace is where the focused tile id is in its column, for restore.
func (w *Workspace) tilePlace(id WindowID) *origPlace {
	c := w.Columns[w.Focus]
	lone := len(c.Windows) == 1
	back := &origPlace{col: w.Focus, row: c.Focus, slot: c.Slot, width: c.Width, fullWidth: c.FullWidth && lone, expanded: c.Expanded && lone}
	for _, v := range c.Windows {
		if v != id {
			back.stacked = append(back.stacked, v)
		}
	}
	return back
}

// addStash appends a window to the stash, shows the stash and selects it.
func (w *Workspace) addStash(f Float) {
	w.Stash = append(w.Stash, f)
	w.stashAt = len(w.Stash) - 1
	w.showStash()
}

// captureStash puts a new window id in the shown stash, selected, and
// reports whether it did. Like a new tile, it leaves a focused native
// float focused. Under a covering fullscreen window the stash is not on
// screen: the window tiles as usual.
func (w *Workspace) captureStash(id WindowID) bool {
	if id == 0 || w.stashHidden || len(w.Stash) == 0 || w.cover() != 0 {
		return false
	}
	// No former column: toggle-window-stash tiles it as a new column.
	w.Stash = append(w.Stash, Float{ID: id})
	w.stashAt, w.stashFocus = len(w.Stash)-1, true
	return true
}

// showStash shows the stash and gives it the focus; a member whose
// fullscreen waited while hidden is fullscreen again, selected, as if it
// asked now.
func (w *Workspace) showStash() {
	w.stashHidden, w.stashFocus, w.floatFocus = false, true, false
	if i := w.stashIndex(w.hiddenFullscreen); i >= 0 {
		w.fullscreen, w.stashAt = w.hiddenFullscreen, i
	}
	w.hiddenFullscreen = 0
}

// unstash returns stash entry i to its former column, focused; a free
// float floats again where it was.
func (w *Workspace) unstash(i int) {
	f := w.Stash[i]
	w.RemoveWindow(f.ID)
	if f.free {
		w.Floats = append(w.Floats, f)
		w.floatFocus, w.stashFocus = true, false
		return
	}
	w.restore(f.ID, f.back)
}

// holdsStack reports whether c holds a window of the stack left behind.
func (p origPlace) holdsStack(c Column) bool {
	return slices.ContainsFunc(c.Windows, func(v WindowID) bool { return slices.Contains(p.stacked, v) })
}

// restore tiles id back where back says: its row in the column that still
// holds its former neighbors, else a column of its own at its former
// index. It takes the focus.
func (w *Workspace) restore(id WindowID, back *origPlace) {
	w.dropStashOver()
	w.floatFocus, w.stashFocus = false, false
	w.raiseColumns()
	if back == nil {
		w.addColumn(Column{Windows: []WindowID{id}})
		return
	}
	at := min(back.col, len(w.Columns))
	if i := slices.IndexFunc(w.Columns, back.holdsStack); i >= 0 {
		c := &w.Columns[i]
		row := 0
		for _, v := range back.stacked[:back.row] {
			if slices.Contains(c.Windows, v) {
				row++
			}
		}
		c.Windows = slices.Insert(c.Windows, row, id)
		c.Focus = row
		if w.policy().equalCells && w.Focus != i {
			w.unmaximize()
		}
		w.Focus = i
		w.scroll()
		return
	}
	// Its column is gone: it comes back expanded, unless another column
	// was expanded meanwhile.
	expanded := back.expanded && !slices.ContainsFunc(w.Columns, func(c Column) bool { return c.Expanded })
	w.insertColumn(at, Column{Windows: []WindowID{id}, Width: back.width, Slot: back.slot, Expanded: expanded})
	if back.fullWidth {
		w.maximize(w.Focus)
	}
}

// rehome is a stashed window moved to another workspace: its former column
// is not there, so it returns as the last column, at its width.
func (f Float) rehome() Float {
	back := &origPlace{col: math.MaxInt}
	if f.back != nil {
		back.width = f.back.width
	}
	f.back = back
	return f
}

// ToggleStashVisible hides the stash, or shows it and gives it the
// focus. Native floats stay as they are. Over a covering fullscreen
// window it is a user override: the stash shows over it, which stays
// fullscreen behind (composed, no scanout) and returns when the stash
// hides. A stashed fullscreen window is the stash on screen: no-op.
func (w *Workspace) ToggleStashVisible() {
	if len(w.Stash) == 0 {
		return
	}
	if c := w.cover(); c != 0 {
		if w.stashIndex(c) >= 0 {
			return
		}
		if w.stashOverCover() {
			w.dropStashOver()
			return
		}
		// Not showStash: a pending stashed fullscreen must not replace
		// the covering window.
		w.stashOver, w.stashHidden, w.stashFocus, w.floatFocus = c, false, true, false
		return
	}
	if w.stashHidden {
		w.showStash()
		return
	}
	w.stashHidden, w.stashFocus = true, false
}

// stashOverCover reports whether the stash shows over the covering
// fullscreen window (ToggleStashVisible).
func (w *Workspace) stashOverCover() bool {
	return w.stashOver != 0 && w.stashOver == w.cover() && !w.stashHidden && len(w.Stash) > 0
}

// stashShown reports whether the stash is on screen: it holds windows, is
// not hidden, and no covering fullscreen window hides it.
func (w *Workspace) stashShown() bool {
	return len(w.Stash) > 0 && !w.stashHidden && (w.cover() == 0 || w.stashOverCover())
}

// dropStashOver ends the stash override when the focus leaves it: the
// stash hides again under the covering window, as it was.
func (w *Workspace) dropStashOver() {
	if w.stashOverCover() {
		w.stashHidden, w.stashFocus = true, false
	}
	w.stashOver = 0
}

// Click focuses the clicked window. A click on a tile behind the shown
// stash hides the stash, as a click outside a menu closes it.
func (w *Workspace) Click(id WindowID) {
	if !w.stashHidden && len(w.Stash) > 0 && !w.isFloat(id) && w.cover() == 0 {
		w.stashHidden = true
	}
	w.FocusID(id)
}

// stashRect is where the selected stashed window is, border included.
func (w *Workspace) stashRect() Rect {
	u := w.Usable
	fw := min(max(u.W*cmp.Or(w.stashWidth, stashWidth)/100-2*w.border, 0)+2*w.border, u.W)
	fh := min(max(u.H*stashHeight/100-2*w.border, 0)+2*w.border, u.H)
	return Rect{X: u.X + (u.W-fw)/2, Y: u.Y + (u.H-fh)/2, W: fw, H: fh}
}

// appendStash appends the placements of the stash to out: the selected window centred, its
// neighbors gap% of the usable width away, showing what the margins
// leave of them, the others hidden. cover is the covering fullscreen
// window, if any.
func (w *Workspace) appendStash(out []Placement, focusedID, cover WindowID) []Placement {
	u := w.Usable
	center := w.stashRect()
	fw, fh := center.W, center.H
	// Neighbors rest in the margins, never over the selected window: the
	// side with the least room says whether there is one.
	margin := (u.W-fw)/2 - w.stashGapPx()
	// The view is moved by stashView.off pitches, rounded once so every
	// window shares the offset.
	pitch := w.stashPitch(fw)
	shift := int(math.Round(w.stashView.off * float64(pitch)))
	for i, f := range w.Stash {
		p := Placement{ID: f.ID, Floating: true, Focused: f.ID == focusedID, Inset: ports.SideAll}
		d := i - w.stashAt
		r := Rect{X: center.X + d*pitch - shift, Y: center.Y, W: fw, H: fh}
		switch {
		case f.ID == cover:
			p.Rect, p.Fullscreen, p.Inset = w.Output, true, 0
		case w.stashHidden || cover != 0 && !w.stashOverCover():
			p.Hidden = true
		case d == 0:
			// The selection is no peek, but its veil grows as it slides.
			p.Rect, p.Veil = r, w.stashVeil(d)
		case (shift != 0 || margin > 0 && (d == -1 || d == 1)) && r.Overlaps(u):
			// A neighbor shows in its margin, if it leaves one. A slide
			// shows what the view moves in, and keeps what it moves out
			// until it is gone.
			p.Rect = r
			p.peeking(w.stashVeil(d))
		default:
			p.Hidden = true
		}
		out = append(out, p)
	}
	return out
}

// stashGapPx is the space between the selected stash window and its
// neighbors, in pixels.
func (w *Workspace) stashGapPx() int { return w.Usable.W * w.stashGap / 100 }

// stashPitch is the distance between two stash windows of width fw.
func (w *Workspace) stashPitch(fw int) int { return max(fw+w.stashGapPx(), 1) }

// stashSpring lands a stash slide from off (in stash windows) with the
// speed velocity; it snaps under half a pixel like the other slides.
func (w *Workspace) stashSpring(off, velocity float64) spring {
	s := workspaceSpring(off, velocity)
	s.Snap = pixelSnap / float64(w.stashPitch(w.stashRect().W))
	return s
}

// stashVeil is how much of the peek veil the stash window d places away
// from stashAt draws: none at the centre of the view, all of it one window
// away or more.
func (w *Workspace) stashVeil(d int) float64 {
	return min(math.Abs(float64(d)-w.stashView.off), 1)
}
