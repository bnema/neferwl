package layout

import "slices"

// Cell is the 1-based column and row of a tiled window, 0, 0 otherwise.
func (w *Lanes) Cell(id WindowID) (col, row int) {
	for ci, c := range w.Columns {
		if ri := slices.Index(c.Windows, id); ri >= 0 {
			return ci + 1, ri + 1
		}
	}
	return 0, 0
}
