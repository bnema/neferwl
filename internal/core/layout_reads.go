package core

// lanes forwards pure reads through a snapshot.
func (w *lanes) band(i int) int                       { v := w.lview(); return v.band(i) }
func (w *lanes) bandBounds(b int) (int, int)          { v := w.lview(); return v.bandBounds(b) }
func (w *lanes) walkBands(f func(int, int, int) bool) { v := w.lview(); v.walkBands(f) }
func (w *lanes) startsBand(i, n int) bool             { v := w.lview(); return v.startsBand(i, n) }
func (w *lanes) bandStart(i int) bool                 { v := w.lview(); return v.bandStart(i) }

func (w *lanes) cellWidth(i int) int { v := w.lview(); return v.cellWidth(i) }

func (w *lanes) equalShare(k int) int           { v := w.lview(); return v.equalShare(k) }
func (w *lanes) laneRow() bool                  { v := w.lview(); return v.laneRow() }
func (w *lanes) inSpiral() bool                 { v := w.lview(); return v.inSpiral() }
func (w *lanes) laneOf(i int) int               { v := w.lview(); return v.laneOf(i) }
func (w *lanes) laneBounds(lane int) (int, int) { v := w.lview(); return v.laneBounds(lane) }
func (w *lanes) laneNeighbor(d int) int         { v := w.lview(); return v.laneNeighbor(d) }
func (w *lanes) validSpans(first, last int) bool {
	v := w.lview()
	return v.validSpans(first, last)
}
func (w *lanes) hiddenByMaximized(i int) bool { v := w.lview(); return v.hiddenByMaximized(i) }
func (w *lanes) fullscreenColumn(i int) bool  { v := w.lview(); return v.fullscreenColumn(i) }
func (w *lanes) columnEdges() ([]int, []int)  { v := w.lview(); return v.columnEdges() }

func (v *layoutView) gap() int { return v.Gap }
