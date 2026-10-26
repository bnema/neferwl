package core

import "github.com/bnema/neferwl/internal/core/layout"

func (w *Workspace) columnWidth(i int) int {
	return w.columnWidthFor(i, false)
}

func (w *Workspace) widthFacts() layout.WidthFacts { v := w.Snapshot(); return v.WidthFactsOf() }

// registeredLayouts is the table as a slice, for tests that run over all layouts.
func registeredLayouts() []layout.Layout {
	var all []layout.Layout
	for _, name := range layout.LayoutNames() {
		l, _ := layout.Lookup(Overflow(name))
		all = append(all, l)
	}
	return all
}

func (w *Workspace) scrollWidth(i int, f layout.WidthFacts) int {
	v := w.Snapshot()
	return v.ScrollWidth(i, f)
}
func (w *Workspace) scrollSpansValid() bool { v := w.Snapshot(); return v.ScrollSpansValid() }
func (w *Workspace) columnWidthWith(i int, ignore bool, f layout.WidthFacts) int {
	v := w.Snapshot()
	return v.ColumnWidthWith(i, ignore, f)
}

func mustLayout(o Overflow) layout.Layout {
	l, ok := layout.Lookup(o)
	if !ok {
		panic(o)
	}
	return l
}
