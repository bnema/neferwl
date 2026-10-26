package core

// Workspace forwards pure reads through a snapshot; layout helpers never need
// floating, stash, or presentation state.
func (w *Workspace) band(i int) int                       { v := w.lview(); return v.band(i) }
func (w *Workspace) bandBounds(b int) (int, int)          { v := w.lview(); return v.bandBounds(b) }
func (w *Workspace) walkBands(f func(int, int, int) bool) { v := w.lview(); v.walkBands(f) }
func (w *Workspace) startsBand(i, n int) bool             { v := w.lview(); return v.startsBand(i, n) }
func (w *Workspace) bandStart(i int) bool                 { v := w.lview(); return v.bandStart(i) }
func (w *Workspace) cascadeBands() (int, int)             { v := w.lview(); return v.cascadeBands() }
func (w *Workspace) cellWidth(i int) int                  { v := w.lview(); return v.cellWidth(i) }
func (w *Workspace) laneStats(first, last int) (int, int, int) {
	v := w.lview()
	return v.laneStats(first, last)
}
func (w *Workspace) laneWeight(i, s, u int) int { v := w.lview(); return v.laneWeight(i, s, u) }
func (w *Workspace) laneWidth(i, first, last, s, u, sum int, valid bool) int {
	v := w.lview()
	return v.laneWidth(i, first, last, s, u, sum, valid)
}
func (w *Workspace) equalShare(k int) int           { v := w.lview(); return v.equalShare(k) }
func (w *Workspace) laneRow() bool                  { v := w.lview(); return v.laneRow() }
func (w *Workspace) inSpiral() bool                 { v := w.lview(); return v.inSpiral() }
func (w *Workspace) laneOf(i int) int               { v := w.lview(); return v.laneOf(i) }
func (w *Workspace) laneBounds(lane int) (int, int) { v := w.lview(); return v.laneBounds(lane) }
func (w *Workspace) laneNeighbor(d int) int         { v := w.lview(); return v.laneNeighbor(d) }
func (w *Workspace) validSpans(first, last int) bool {
	v := w.lview()
	return v.validSpans(first, last)
}
func (w *Workspace) hiddenByMaximized(i int) bool { v := w.lview(); return v.hiddenByMaximized(i) }
func (w *Workspace) fullscreenColumn(i int) bool  { v := w.lview(); return v.fullscreenColumn(i) }
func (w *Workspace) columnEdges() ([]int, []int)  { v := w.lview(); return v.columnEdges() }

func (v *layoutView) gap() int { return v.Gap }
