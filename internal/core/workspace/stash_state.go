package workspace

import "github.com/bnema/neferwl/internal/core/motion"

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
// fullscreen; otherwise it keeps its own.
func (s *Stash) Absorb(src *Stash, prefer bool) {
	if src == s || len(src.entries) == 0 {
		*src = Stash{}
		return
	}
	n := len(s.entries)
	s.entries = append(s.entries, src.entries...)
	if prefer || n == 0 {
		s.at, s.view = n+src.at, src.view
		s.focus, s.hidden, s.fullscreen = src.focus, src.hidden, src.fullscreen
	}
	*src = Stash{}
}

// Lend makes w use the shared stash s; nil returns it to its own. A
// fullscreen member of the stash it leaves stays pending in that stash and
// follows it; its own windows join s, so none shows in two stashes. On a
// shown s, a pending fullscreen member covers w when nothing else does,
// and a focused s keeps the focus.
func (w *Workspace) Lend(s *Stash) {
	if s == &w.own {
		s = nil
	}
	if s == w.lent {
		return
	}
	old := w.st()
	// Held members of the leaving stash are no longer on w.
	for i := range old.entries {
		w.dropHeld(old.entries[i].ID)
	}
	if w.Fullscreen != 0 && w.StashIndex(w.Fullscreen) >= 0 {
		old.fullscreen = w.Fullscreen
		w.Fullscreen = 0
	}
	w.stashOver = 0
	w.lent = s
	if s != nil {
		s.Absorb(&w.own, false)
	}
	cur := w.st()
	if len(cur.entries) > 0 && !cur.hidden {
		if i := w.StashIndex(cur.fullscreen); i >= 0 && w.Fullscreen == 0 {
			w.Fullscreen, cur.at, cur.fullscreen = cur.fullscreen, i, 0
		}
		if cur.focus {
			w.floatFocus = false
		}
	}
	w.reconcileFloats()
	w.scroll()
}
