package core

func (w *Workspace) columnWidth(i int) int {
	return w.columnWidthFor(i, false)
}

func (w *Workspace) widthFacts() widthFacts { v := w.lview(); return v.widthFactsOf() }

// registeredLayouts is the table as a slice, for tests that run over all layouts.
func registeredLayouts() []layout { return layoutTable[:] }

func (w *Workspace) scrollWidth(i int, f widthFacts) int { v := w.lview(); return v.scrollWidth(i, f) }
func (w *Workspace) scrollSpansValid() bool              { v := w.lview(); return v.scrollSpansValid() }
func (w *Workspace) columnWidthWith(i int, ignore bool, f widthFacts) int {
	v := w.lview()
	return v.columnWidthWith(i, ignore, f)
}
