package layout

// OverflowScroll opens further columns to the right; the view scrolls.
const OverflowScroll Overflow = "scroll"

// scrollLayout opens further columns to the right; the view scrolls. It is
// the neutral Base.
type scrollLayout struct{ Base }

// Name returns the configuration key.
func (scrollLayout) Name() Overflow { return OverflowScroll }
