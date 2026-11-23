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
