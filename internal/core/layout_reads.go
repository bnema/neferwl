package core

// lanes forwards pure reads through a snapshot.
func (w *Lanes) Band(i int) int { v := w.Snapshot(); return v.band(i) }

// BandBounds returns the inclusive bounds of a band.
func (w *Lanes) BandBounds(b int) (int, int) { v := w.Snapshot(); return v.bandBounds(b) }

// WalkBands visits bands in order until the callback returns false.
func (w *Lanes) WalkBands(f func(int, int, int) bool) { v := w.Snapshot(); v.walkBands(f) }

// StartsBand reports whether a column starts a band of capacity n.
func (w *Lanes) StartsBand(i, n int) bool { v := w.Snapshot(); return v.startsBand(i, n) }

// BandStart reports whether a column has a stored band boundary.
func (w *Lanes) BandStart(i int) bool { v := w.Snapshot(); return v.bandStart(i) }

// CellWidth returns the width of a column within its lane.
func (w *Lanes) CellWidth(i int) int { v := w.Snapshot(); return v.cellWidth(i) }

// EqualShare returns one of k equal shares of usable width.
func (w *Lanes) EqualShare(k int) int { v := w.Snapshot(); return v.equalShare(k) }

// LaneRow reports whether columns share a row by cells.
func (w *Lanes) LaneRow() bool { v := w.Snapshot(); return v.LaneRow() }

// InSpiral reports whether excess columns subdivide the last slot.
func (w *Lanes) InSpiral() bool { v := w.Snapshot(); return v.InSpiral() }

// LaneOf returns the lane containing a column.
func (w *Lanes) LaneOf(i int) int { v := w.Snapshot(); return v.LaneOf(i) }

// LaneBounds returns inclusive column bounds; first > last denotes no lane.
func (w *Lanes) LaneBounds(lane int) (int, int) { v := w.Snapshot(); return v.LaneBounds(lane) }

// LaneNeighbor selects the closest column in an adjacent lane, or -1.
func (w *Lanes) LaneNeighbor(d int) int { v := w.Snapshot(); return v.LaneNeighbor(d) }

// ValidSpans reports whether a lane has complete manual widths.
func (w *Lanes) ValidSpans(first, last int) bool {
	v := w.Snapshot()
	return v.validSpans(first, last)
}

// HiddenByMaximized reports whether maximization hides column i.
func (w *Lanes) HiddenByMaximized(i int) bool { v := w.Snapshot(); return v.HiddenByMaximized(i) }

// FullscreenColumn reports whether column i contains the fullscreen window.
func (w *Lanes) FullscreenColumn(i int) bool { v := w.Snapshot(); return v.FullscreenColumn(i) }

// ColumnEdges returns unscrolled positions and widths in one pass.
func (w *Lanes) ColumnEdges() ([]int, []int) { v := w.Snapshot(); return v.ColumnEdges() }

func (v *View) gap() int { return v.Gap }
