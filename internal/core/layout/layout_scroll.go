package layout

import "slices"

// OverflowScroll opens further columns to the right; the view scrolls.
const OverflowScroll Overflow = "scroll"

// scrollLayout opens further columns to the right; the view scrolls. It is
// the neutral Base.
type scrollLayout struct{ Base }

// Name returns the configuration key.
func (scrollLayout) Name() Overflow { return OverflowScroll }

// AdoptCoverFloat tiles a fullscreen float only for a same-app arrival.
// Ordinary fullscreen requests retain their existing floating semantics.
func (scrollLayout) AdoptCoverFloat(w Edit, id WindowID) bool {
	at := min(w.Focus+1, len(w.Columns))
	w.Columns = slices.Insert(w.Columns, at, Column{Windows: []WindowID{id}})
	w.ColumnInserted(at, InsertDefault)
	w.Focus = at
	return true
}
