package workspace

const (
	// Drop geometry is measured in output-local logical pixels.
	dropBandDiv = 4
	gapPad      = 12
	edgeScroll  = 32
	// Overview geometry: the card zoom, the card count and step, the peek step
	// and the zoom bounds of a row.
	overviewCardZoom = 0.2
	overviewCards    = 4
	overviewCardStep = 8
	overviewPeekStep = 0.07
	overviewMaxZoom  = 0.6
	overviewMinZoom  = 0.25
)

// IsFloat reports whether id is a native floating or stashed window. It is
// two index lookups and computes no rectangle.
func (w *Workspace) IsFloat(id WindowID) bool {
	return w.floatIndex(id) >= 0 || w.StashIndex(id) >= 0
}
