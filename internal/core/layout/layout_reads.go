package layout

// Band returns the band containing column i.
func (w *Lanes) Band(i int) int { v := w.Snapshot(); return v.band(i) }

// bandBounds returns the inclusive bounds of a band.
func (w *Lanes) bandBounds(b int) (int, int) { v := w.Snapshot(); return v.bandBounds(b) }

// WalkBands visits bands in order until the callback returns false.
func (w *Lanes) WalkBands(f func(int, int, int) bool) { v := w.Snapshot(); v.walkBands(f) }

// EqualShare returns one of k equal shares of usable width.
func (w *Lanes) EqualShare(k int) int { v := w.Snapshot(); return v.equalShare(k) }

// laneRow reports whether columns share a row by cells.
func (w *Lanes) laneRow() bool { v := w.Snapshot(); return v.laneRow() }

// InSpiral reports whether excess columns subdivide the last slot.
func (w *Lanes) InSpiral() bool { v := w.Snapshot(); return v.inSpiral() }

// LaneOf returns the lane containing a column.
func (w *Lanes) LaneOf(i int) int { v := w.Snapshot(); return v.laneOf(i) }

// LaneBounds returns inclusive column bounds; first > last denotes no lane.
func (w *Lanes) LaneBounds(lane int) (int, int) { v := w.Snapshot(); return v.laneBounds(lane) }

// LaneNeighbor selects the closest column in an adjacent lane, or -1.
func (w *Lanes) LaneNeighbor(d int) int { v := w.Snapshot(); return v.laneNeighbor(d) }

// ValidSpans reports whether a lane has complete manual widths.
func (w *Lanes) ValidSpans(first, last int) bool {
	v := w.Snapshot()
	return v.validSpans(first, last)
}

// FullscreenColumn reports whether column i contains the fullscreen window.
// It reads the lane fields directly: AppendLayout asks it per column every
// frame, and a Snapshot per call would copy the whole View.
func (w *Lanes) FullscreenColumn(i int) bool { return fullscreenColumn(w.Columns[i], w.Fullscreen) }

// ColumnEdges returns unscrolled positions and widths in one pass.
func (w *Lanes) ColumnEdges() ([]int, []int) { v := w.Snapshot(); return v.columnEdges() }

func (v *View) gap() int { return v.Gap }
