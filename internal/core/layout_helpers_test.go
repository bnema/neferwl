package core

func (w *Workspace) columnWidth(i int) int {
	return w.columnWidthFor(i, false)
}

func (w *Workspace) widthFacts() widthFacts { return w.widthFactsOf(w.lview()) }

// registeredLayouts is the table as a slice, for tests that run over all layouts.
func registeredLayouts() []layout { return layoutTable[:] }
