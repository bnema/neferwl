package core

// Default is the layout of an empty or unknown overflow value.
const Default = OverflowScroll

// layoutTable is the one list of layouts. Adding a layout is one entry here.
var layoutTable = [...]Layout{scrollLayout{}, fixedLayout{}, cascadeLayout{}}

// Lookup resolves a config value; "" is the default layout. A scan of the
// table over zero-size layout values: no map, no allocation.
func Lookup(o Overflow) (Layout, bool) {
	if o == "" {
		o = Default
	}
	for _, l := range layoutTable {
		if l.Name() == o {
			return l, true
		}
	}
	return nil, false
}

// LayoutNames lists the config values of every layout, in table order.
func LayoutNames() []string {
	names := make([]string, len(layoutTable))
	for i, l := range layoutTable {
		names[i] = string(l.Name())
	}
	return names
}

// resolveLayout is the layout of an overflow value: the registry's, or the
// default for an unknown value.
func resolveLayout(o Overflow) Layout {
	if l, ok := Lookup(o); ok {
		return l
	}
	l, _ := Lookup(Default)
	return l
}

// SetOverflow is the one writer of Overflow: it resolves the layout once so
// the hot paths do not scan the table.
func (w *Lanes) SetOverflow(o Overflow) {
	w.Overflow = o
	w.lstateReady = false
	w.lcache, w.lcacheFor = resolveLayout(o), o
}

// Engine resolves Overflow lazily and caches the result until Overflow changes.
// Override wins over the registry. Like all lane operations, only the owner
// goroutine may call it.
func (w *Lanes) Engine() Layout {
	if w.Override != nil {
		return w.Override
	}
	if w.lcache == nil || w.lcacheFor != w.Overflow {
		w.lstateReady = false
		w.lcache, w.lcacheFor = resolveLayout(w.Overflow), w.Overflow
	}
	return w.lcache
}
