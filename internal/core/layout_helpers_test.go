package core

func (w *Workspace) columnWidth(i int) int {
	return w.columnWidthFor(i, false)
}

func (w *Workspace) widthFacts() WidthFacts { v := w.Snapshot(); return v.WidthFactsOf() }

// registeredLayouts is the table as a slice, for tests that run over all layouts.
func registeredLayouts() []Layout { return layoutTable[:] }

func (w *Workspace) scrollWidth(i int, f WidthFacts) int {
	v := w.Snapshot()
	return v.ScrollWidth(i, f)
}
func (w *Workspace) scrollSpansValid() bool { v := w.Snapshot(); return v.ScrollSpansValid() }
func (w *Workspace) columnWidthWith(i int, ignore bool, f WidthFacts) int {
	v := w.Snapshot()
	return v.ColumnWidthWith(i, ignore, f)
}
