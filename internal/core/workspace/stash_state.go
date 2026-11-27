package workspace

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/core/motion"
	"github.com/bnema/neferwl/internal/domain"
)

// Stash is the state of a stash: the windows set aside by
// toggle-window-stash and how they show. A workspace reads it through st.
type Stash struct {
	// entries holds the stashed windows, left to right (stash.go); at is
	// the selected one.
	entries []Float
	at      int
	// view slides the stash: the selected window is drawn at at+view.off,
	// in stash windows. Pure presentation over at.
	view motion.Slide
	// focus is set while the stash has the focus, under a focused native
	// float; hidden while the user hid it.
	focus, hidden bool
	// fullscreen holds a stashed window's fullscreen while the stash is
	// hidden, restored when shown.
	fullscreen WindowID
}

// Borrows reports whether s is the shared stash lent to w.
func (w *Workspace) Borrows(s *Stash) bool { return w.lent == s }

// st returns the stash the workspace uses: the lent one, else its own.
func (w *Workspace) st() *Stash {
	if w.lent != nil {
		return w.lent
	}
	return &w.own
}

// Len returns the number of stashed windows.
func (s *Stash) Len() int { return len(s.entries) }

// Absorb appends src's windows to s and leaves src empty. With prefer, or
// when s was empty, s takes src's selection, focus, visibility and pending
// fullscreen when it has one; otherwise it keeps its own state and takes
// src's pending fullscreen only into an empty slot.
func (s *Stash) Absorb(src *Stash, prefer bool) {
	if src == s || len(src.entries) == 0 {
		*src = Stash{}
		return
	}
	n := len(s.entries)
	s.entries = append(s.entries, src.entries...)
	// One pending fullscreen slot: the preferred side's wins, else the
	// other's is kept. A loser is placed as a plain stash window, which
	// tells its client it left fullscreen.
	if prefer || n == 0 {
		s.at, s.view = n+src.at, src.view
		s.focus, s.hidden = src.focus, src.hidden
		s.fullscreen = cmp.Or(src.fullscreen, s.fullscreen)
	} else {
		s.fullscreen = cmp.Or(s.fullscreen, src.fullscreen)
	}
	*src = Stash{}
}

// absorbKeep appends src's windows to s, keeping s's state (selection,
// view, focus, visibility) and its pending fullscreen, else src's; src is
// left empty.
func (s *Stash) absorbKeep(src *Stash) {
	if src == s {
		return
	}
	s.entries = append(s.entries, src.entries...)
	s.fullscreen = cmp.Or(s.fullscreen, src.fullscreen)
	*src = Stash{}
}

// Lend makes w use the shared stash s; nil returns it to its own. The
// stash it leaves is detached (detachStash), its own windows join s
// (GiveStash), so none shows in two stashes, and s is attached
// (attachStash).
func (w *Workspace) Lend(s *Stash) {
	if s == &w.own {
		s = nil
	}
	if s == w.lent {
		return
	}
	w.detachStash(w.st())
	if s != nil && w.lent == nil {
		w.GiveStash(s, false)
	}
	w.lent = s
	w.attachStash()
}

// attachStash binds the stash w now sees: when shown, a pending fullscreen
// member covers w (promotePending), it shows over another covering window,
// and a focused stash keeps the focus. Only the cover follows a stash:
// held fullscreen members of a stash w left dropped out of the group.
func (w *Workspace) attachStash() {
	cur := w.st()
	if len(cur.entries) > 0 && !cur.hidden {
		w.promotePending()
		if c := w.Cover(); c != 0 && w.StashIndex(c) < 0 {
			w.showStashOver()
		} else if cur.focus {
			w.floatFocus = false
		}
	}
	w.reconcileFloats()
	w.scroll()
}

// TakeStash returns w to its own stash (Lend(nil)) and absorbs src into
// it, src's selection and state preferred, then attaches the result as
// Lend does. src is left empty.
func (w *Workspace) TakeStash(src *Stash) {
	w.Lend(nil)
	if src == &w.own || src.Len() == 0 {
		return
	}
	w.own.Absorb(src, true)
	w.attachStash()
}

// GiveStash moves every window of the stash w sees into dst and reports
// whether there was any. The stash is detached from w first (detachStash)
// and merged by Absorb, prefer choosing whose state dst keeps; w's stash
// is left empty.
func (w *Workspace) GiveStash(dst *Stash, prefer bool) bool {
	src := w.st()
	if src == dst || len(src.entries) == 0 {
		// Nothing to hand over: an empty stash binds nothing to w.
		return false
	}
	w.detachStash(src)
	dst.Absorb(src, prefer)
	w.reconcileFloats()
	w.scroll()
	return true
}

// Reattach is run on the workspace a stash is lent to after windows were
// absorbed into it: a shown stash's pending fullscreen member covers w
// when nothing else does, as when it was lent.
func (w *Workspace) Reattach() {
	if w.promotePending() {
		w.reconcileFloats()
		w.scroll()
	}
}

// promotePending makes a shown stash's pending fullscreen member w's
// fullscreen, selected, when w has none (even one scrolled off), and
// reports whether it did.
func (w *Workspace) promotePending() bool {
	s := w.st()
	if len(s.entries) == 0 || s.hidden || w.Fullscreen != 0 {
		return false
	}
	i := w.StashIndex(s.fullscreen)
	if i < 0 {
		return false
	}
	w.Fullscreen, s.at, s.fullscreen = s.fullscreen, i, 0
	return true
}

// detachStash unbinds stash s from w without touching its visibility or
// focus, which belong to the stash: its held members leave the group, a
// fullscreen member waits in s's pending slot to follow it, and the
// override over w's cover ends.
func (w *Workspace) detachStash(s *Stash) {
	for i := range s.entries {
		w.dropHeld(s.entries[i].ID)
	}
	if w.Fullscreen != 0 && slices.ContainsFunc(s.entries, func(f Float) bool { return f.ID == w.Fullscreen }) {
		s.fullscreen = w.Fullscreen
		w.Fullscreen = 0
	}
	w.stashOver = 0
}

// TagHome marks every window of s without a home as coming from the
// monitor ref.
func (s *Stash) TagHome(ref domain.MonitorRef) {
	for i := range s.entries {
		if s.entries[i].home == (domain.MonitorRef{}) {
			s.entries[i].home = ref
		}
	}
}

// TakeHome moves the windows of s whose home matches into dst, in order,
// home cleared, and keeps the selection of s on the same window, else the
// nearest one. A pending fullscreen moves with its window. An empty dst
// that receives windows starts hidden: they return without covering the
// screen. It reports whether any window moved. Rare (replug): it may
// allocate.
func (s *Stash) TakeHome(dst *Stash, matches func(home domain.MonitorRef) bool) bool {
	if s == dst {
		return false
	}
	empty := len(dst.entries) == 0
	var sel WindowID
	if s.at < len(s.entries) {
		sel = s.entries[s.at].ID
	}
	kept, moved := s.entries[:0], false
	for _, f := range s.entries {
		if f.home == (domain.MonitorRef{}) || !matches(f.home) {
			kept = append(kept, f)
			continue
		}
		f.home = domain.MonitorRef{}
		if f.ID == s.fullscreen {
			dst.fullscreen, s.fullscreen = f.ID, 0
		}
		dst.entries = append(dst.entries, f)
		moved = true
	}
	clear(s.entries[len(kept):])
	s.entries = kept
	if !moved {
		return false
	}
	at := slices.IndexFunc(kept, func(f Float) bool { return f.ID == sel })
	if at < 0 {
		at = min(s.at, max(len(kept)-1, 0))
	}
	s.at, s.view = at, motion.Slide{}
	if len(kept) == 0 {
		*s = Stash{}
	}
	if empty {
		dst.at, dst.hidden, dst.focus = 0, true, false
	}
	return true
}

// ClearHome forgets the home of the windows of s whose home matches: they
// are home already. Order, selection and visibility are unchanged.
func (s *Stash) ClearHome(matches func(home domain.MonitorRef) bool) {
	for i := range s.entries {
		if h := s.entries[i].home; h != (domain.MonitorRef{}) && matches(h) {
			s.entries[i].home = domain.MonitorRef{}
		}
	}
}
