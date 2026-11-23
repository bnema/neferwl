package workspace

import (
	"cmp"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
	"github.com/bnema/neferwl/internal/domain"
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

// StashLen returns the number of stashed windows.
func (w *Workspace) StashLen() int { return len(w.st().entries) }

// StashID returns the window at stash index i.
func (w *Workspace) StashID(i int) WindowID { return w.st().entries[i].ID }

// StashIndex returns the stash index of id, or -1.
func (w *Workspace) StashIndex(id WindowID) int {
	return slices.IndexFunc(w.st().entries, func(f Float) bool { return f.ID == id })
}

// stashFocused reports whether the stash has the focus, under any native
// float that has it. A shown stash without tiles always has it.
func (w *Workspace) stashFocused() bool {
	return (w.st().focus || len(w.Columns) == 0) && !w.st().hidden && len(w.st().entries) > 0
}

// OnFloat reports whether the focus is on a floating window: column
// actions do nothing then.
func (w *Workspace) OnFloat() bool { return w.floatFocus || w.stashFocused() }

// stashWindow moves the focused tile id to the end of the stash,
// remembering its place in its column to return there.
func (w *Workspace) stashWindow(id WindowID) {
	back := w.tilePlace(id)
	w.RemoveWindow(id)
	w.addStash(Float{ID: id, back: back, origin: w.ID})
}

// tilePlace is where the focused tile id is in its column, for restore.
func (w *Workspace) tilePlace(id WindowID) *origPlace {
	c := w.Columns[w.Focus]
	lone := len(c.Windows) == 1
	back := &origPlace{col: w.Focus, row: c.Focus, slot: c.Slot, cells: c.Cells, fullWidth: c.FullWidth && lone, tabbed: c.Tabbed}
	for _, v := range c.Windows {
		if v != id {
			back.stacked = append(back.stacked, v)
		}
	}
	return back
}

// addStash appends a window to the stash, shows the stash and selects it.
func (w *Workspace) addStash(f Float) {
	w.st().entries = append(w.st().entries, f)
	w.st().at = len(w.st().entries) - 1
	w.showStash()
}

// CaptureStash puts a new window id in the shown stash, selected, and
// reports whether it did. Like a new tile, it leaves a focused native
// float focused. A stash shown over a covering fullscreen window captures
// too; one hidden under the cover does not, and the window tiles.
func (w *Workspace) CaptureStash(id WindowID) bool {
	if id == 0 || !w.StashCaptures() {
		return false
	}
	// No former column: toggle-window-stash tiles it as a new column.
	w.st().entries = append(w.st().entries, Float{ID: id})
	w.st().at, w.st().focus = len(w.st().entries)-1, true
	return true
}

// StashCaptures reports whether a new window would be captured by the
// stash (CaptureStash): whenever the stash is on screen (StashShown),
// including shown over a covering fullscreen window.
func (w *Workspace) StashCaptures() bool { return w.StashShown() }

// StashSpawned puts a window opened by a stash spawn in the stash, selected
// and focused, with no former column: toggle-window-stash tiles it as a new
// column. A fullscreen stash member, current or waiting for the stash to
// show, ends first, so the newcomer is the window on screen. Under a
// covering fullscreen window outside the stash, the stash shows over it.
func (w *Workspace) StashSpawned(id WindowID) {
	if w.Fullscreen != 0 && w.StashIndex(w.Fullscreen) >= 0 {
		w.endFullscreen()
	}
	w.st().fullscreen = 0
	w.addStash(Float{ID: id})
	if w.Cover() != 0 {
		w.showStashOver()
	}
}

// showStash shows the stash and gives it the focus; a member whose
// fullscreen waited while hidden is fullscreen again, selected, as if it
// asked now.
func (w *Workspace) showStash() {
	w.st().hidden, w.st().focus, w.floatFocus = false, true, false
	if i := w.StashIndex(w.st().fullscreen); i >= 0 {
		w.Fullscreen, w.st().at = w.st().fullscreen, i
	}
	w.st().fullscreen = 0
}

// unstash returns stash entry i to its former column, focused; a free
// float floats again where it was.
func (w *Workspace) unstash(i int) {
	f := w.st().entries[i]
	w.RemoveWindow(f.ID)
	if f.free {
		f.home = ""
		w.Floats = append(w.Floats, f)
		w.floatFocus, w.st().focus = true, false
		return
	}
	if f.origin != 0 && f.origin != w.ID {
		// Stashed on another workspace: its former place is not here. It
		// tiles right of the focused column, at its width.
		place := &origPlace{}
		if len(w.Columns) > 0 {
			place.col = w.Focus + 1
		}
		if f.back != nil {
			place.cells = f.back.cells
		}
		w.restore(f.ID, place)
		return
	}
	w.restore(f.ID, f.back)
}

// holdsStack reports whether c holds a window of the stack left behind.
func (p origPlace) holdsStack(c layout.Column) bool {
	return slices.ContainsFunc(c.Windows, func(v WindowID) bool { return slices.Contains(p.stacked, v) })
}

// restore tiles id back where back says: its row in the column that still
// holds its former neighbors, else a column of its own at its former
// index. It takes the focus.
func (w *Workspace) restore(id WindowID, back *origPlace) {
	w.dropStashOver()
	w.floatFocus, w.st().focus = false, false
	w.raiseColumns()
	if back == nil {
		w.addColumn(layout.Column{Windows: []WindowID{id}})
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
		// Re-tab only what Normalize untabbed when the column fell to one
		// window; a column the user untabbed or a stack stays as it is.
		retab := back.tabbed && !c.Tabbed && len(c.Windows) == 1
		c.Windows = slices.Insert(c.Windows, row, id)
		c.Focus = row
		if retab {
			c.Tabbed, c.Shares = true, nil
		}
		if w.Engine().MaximizeHides() && w.Focus != i {
			w.Unmaximize()
		}
		w.Focus = i
		w.scroll()
		return
	}
	// Its column is gone: it comes back at its width.
	w.insertColumn(at, layout.Column{Windows: []WindowID{id}, Cells: back.cells, Slot: back.slot})
	if back.fullWidth {
		w.Maximize(w.Focus)
	}
}

// rehome is a stashed window moved to another workspace: its former column
// is not there, so it returns as the last column, at its width.
func (f Float) rehome() Float {
	back := &origPlace{col: math.MaxInt}
	if f.back != nil {
		back.cells = f.back.cells
	}
	f.back, f.origin, f.home = back, 0, ""
	return f
}

// ToggleStashVisible hides the stash, or shows it and gives it the
// focus. Native floats stay as they are. Over a covering fullscreen
// window it is a user override: the stash shows over it, which stays
// fullscreen behind (composed, no scanout) and returns when the stash
// hides. A stashed fullscreen window is the stash on screen: no-op.
func (w *Workspace) ToggleStashVisible() {
	if len(w.st().entries) == 0 {
		return
	}
	if c := w.Cover(); c != 0 {
		if w.StashIndex(c) >= 0 {
			return
		}
		if w.stashOverCover() {
			w.dropStashOver()
			return
		}
		w.showStashOver()
		return
	}
	if w.st().hidden {
		w.showStash()
		return
	}
	w.st().hidden, w.st().focus = true, false
}

// ShowStash shows the stash and gives it the focus, hidden or not; over a
// covering window outside the stash it shows over it. A stashed cover is
// the stash on screen already: no-op.
func (w *Workspace) ShowStash() {
	if len(w.st().entries) == 0 {
		return
	}
	if c := w.Cover(); c != 0 {
		if w.StashIndex(c) < 0 {
			w.showStashOver()
		}
		return
	}
	w.showStash()
}

// showStashOver shows the stash over the covering fullscreen window,
// focused. Not showStash: a pending stashed fullscreen must not replace
// the covering window.
func (w *Workspace) showStashOver() {
	w.stashOver, w.st().hidden, w.st().focus, w.floatFocus = w.Cover(), false, true, false
}

// stashOverCover reports whether the stash shows over the covering
// fullscreen window (ToggleStashVisible, Activate).
func (w *Workspace) stashOverCover() bool {
	return w.stashOver != 0 && w.stashOver == w.Cover() && !w.st().hidden && len(w.st().entries) > 0
}

// StashShown reports whether the stash is on screen: it holds windows, is
// not hidden, and no covering fullscreen window hides it.
func (w *Workspace) StashShown() bool {
	return len(w.st().entries) > 0 && !w.st().hidden && (w.Cover() == 0 || w.stashOverCover())
}

// dropStashOver ends the stash override when the focus leaves it: the
// stash hides again under the covering window, as it was.
func (w *Workspace) dropStashOver() {
	if w.stashOverCover() {
		w.st().hidden, w.st().focus = true, false
	}
	w.stashOver = 0
}

// InputBlocked reports whether id is a column window behind the shown
// stash, including a covering fullscreen tile. Floats and stash members
// remain interactive.
func (w *Workspace) InputBlocked(id WindowID) bool {
	return w.StashShown() && w.ColumnOf(id) >= 0
}

// Click focuses an interactive window. Blocked tiles never reach Click.
func (w *Workspace) Click(id WindowID) {
	w.FocusID(id)
	w.FocusModal(0)
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
	// The view is moved by view.Off pitches, rounded once so every
	// window shares the offset.
	pitch := w.stashPitch(fw)
	shift := int(math.Round(w.st().view.Off * float64(pitch)))
	for i, f := range w.st().entries {
		p := Placement{ID: f.ID, Floating: true, Focused: f.ID == focusedID, Inset: domain.SideAll}
		d := i - w.st().at
		r := Rect{X: center.X + d*pitch - shift, Y: center.Y, W: fw, H: fh}
		switch {
		case f.ID == cover:
			p.Rect, p.Fullscreen, p.Inset = w.Output, true, 0
		case w.st().hidden || cover != 0 && !w.stashOverCover():
			p.Hidden = true
		case d == 0:
			// The selection is no peek, but its veil grows as it slides.
			p.Rect, p.Veil = r, w.stashVeil(d)
		case (shift != 0 || margin > 0 && (d == -1 || d == 1)) && r.Overlaps(u):
			// A neighbor shows in its margin, if it leaves one. A slide
			// shows what the view moves in, and keeps what it moves out
			// until it is gone.
			p.Rect = r
			p.Peeking(w.stashVeil(d))
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

// StashSpring lands a stash slide from off (in stash windows) with the
// speed velocity; it snaps under half a pixel like the other slides.
func (w *Workspace) StashSpring(off, velocity float64) motion.Spring {
	s := motion.WorkspaceSpring(off, velocity)
	s.Snap = motion.PixelSnap / float64(w.stashPitch(w.stashRect().W))
	return s
}

// stashVeil is how much of the peek veil the stash window d places away
// from the selected one draws: none at the centre of the view, all of it
// one window away or more.
func (w *Workspace) stashVeil(d int) float64 {
	return min(math.Abs(float64(d)-w.st().view.Off), 1)
}
