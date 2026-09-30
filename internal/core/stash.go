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
	c := w.Columns[w.Focus]
	lone := len(c.Windows) == 1
	back := &origPlace{col: w.Focus, row: c.Focus, slot: c.Slot, width: c.Width, fullWidth: c.FullWidth && lone, expanded: c.Expanded && lone}
	for _, v := range c.Windows {
		if v != id {
			back.stacked = append(back.stacked, v)
		}
	}
	w.RemoveWindow(id)
	w.addStash(Float{ID: id, back: back})
}

// addStash appends a window to the stash, shows the stash and selects it.
func (w *Workspace) addStash(f Float) {
	w.Stash = append(w.Stash, f)
	w.stashAt = len(w.Stash) - 1
	w.showStash()
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

// unstash returns stash entry i to its former column, focused.
func (w *Workspace) unstash(i int) {
	f := w.Stash[i]
	w.RemoveWindow(f.ID)
	w.restore(f.ID, f.back)
}

// restore tiles id back where back says: its row in the column that still
// holds its former neighbors, else a column of its own at its former
// index. It takes the focus.
func (w *Workspace) restore(id WindowID, back *origPlace) {
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
		w.Focus = i
		w.scroll()
		return
	}
	// Its column is gone: it comes back expanded, unless another column
	// was expanded meanwhile (as leaveFullscreen).
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
// focus. Native floats stay as they are. It does nothing under a covering
// fullscreen window (a game in scanout): hiding it would drop scanout and
// VRR.
func (w *Workspace) ToggleStashVisible() {
	if len(w.Stash) == 0 || w.cover() != 0 {
		return
	}
	if w.stashHidden {
		w.showStash()
		return
	}
	w.stashHidden, w.stashFocus = true, false
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

// stashLayout places the stash: the selected window centred, its
// neighbors gap% of the usable width away, showing what the margins
// leave of them, the others hidden. cover is the covering fullscreen
// window, if any.
func (w *Workspace) stashLayout(focusedID, cover WindowID) []Placement {
	u := w.Usable
	center := w.stashRect()
	fw, fh := center.W, center.H
	// Peeks stay in the margins: they never overlap the selected window.
	gap := u.W * w.stashGap / 100
	peek := max((u.W-fw)/2-gap, 0)
	out := make([]Placement, 0, len(w.Stash))
	for i, f := range w.Stash {
		p := Placement{ID: f.ID, Floating: true, Focused: f.ID == focusedID, Inset: ports.SideAll}
		switch {
		case f.ID == cover:
			p.Rect, p.Fullscreen, p.Inset = w.Output, true, 0
		case w.stashHidden || cover != 0:
			p.Hidden = true
		case i == w.stashAt:
			p.Rect = center
		case peek > 0 && i == w.stashAt-1:
			p.Rect, p.Peek = Rect{X: center.X - gap - fw, Y: center.Y, W: fw, H: fh}, true
		case peek > 0 && i == w.stashAt+1:
			p.Rect, p.Peek = Rect{X: center.X + fw + gap, Y: center.Y, W: fw, H: fh}, true
		default:
			p.Hidden = true
		}
		out = append(out, p)
	}
	return out
}
