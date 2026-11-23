package workspace

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/core/motion"
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

// Lend makes w use the shared stash s; nil returns it to its own. The
// stash it leaves is detached (detachStash) and its own windows join s
// (GiveStash), so none shows in two stashes. On a shown s, a pending
// fullscreen member covers w when nothing else does; under another
// covering window s shows over it; a focused s keeps the focus.
// Only the cover follows the stash: held fullscreen members of the stash
// w leaves drop out of the group. A pending fullscreen waits while w has a
// fullscreen window of its own, even one scrolled off.
func (w *Workspace) Lend(s *Stash) {
	if s == &w.own {
		s = nil
	}
	if s == w.lent {
		return
	}
	if s != nil && w.lent == nil {
		w.GiveStash(s, false)
	} else {
		w.detachStash(w.st())
	}
	w.lent = s
	cur := w.st()
	if len(cur.entries) > 0 && !cur.hidden {
		if i := w.StashIndex(cur.fullscreen); i >= 0 && w.Fullscreen == 0 {
			w.Fullscreen, cur.at, cur.fullscreen = cur.fullscreen, i, 0
		}
		if c := w.Cover(); c != 0 && w.StashIndex(c) < 0 {
			w.showStashOver()
		} else if cur.focus {
			w.floatFocus = false
		}
	}
	w.reconcileFloats()
	w.scroll()
}

// GiveStash moves every window of the stash w sees into dst, detached from
// w first (detachStash), and leaves w's stash empty. With prefer dst takes
// the given stash's selection and state (Absorb).
func (w *Workspace) GiveStash(dst *Stash, prefer bool) {
	src := w.st()
	if src == dst {
		return
	}
	w.detachStash(src)
	dst.Absorb(src, prefer)
	w.reconcileFloats()
	w.scroll()
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
