package core

// OverflowScroll opens further columns to the right; the view scrolls.
const OverflowScroll Overflow = "scroll"

// scrollLayout opens further columns to the right; the view scrolls. It is
// the neutral columnsBase.
type scrollLayout struct{ columnsBase }

func (scrollLayout) name() Overflow { return OverflowScroll }
