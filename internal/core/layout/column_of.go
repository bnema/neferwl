package layout

// ColumnOf returns the column containing id, or -1.
func (w *Lanes) ColumnOf(id WindowID) int {
	c, _ := w.Cell(id)
	return c - 1
}
