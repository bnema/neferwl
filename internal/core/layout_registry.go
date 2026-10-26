package core

// defaultLayout is the layout of an empty or unknown overflow value.
const defaultLayout = OverflowScroll

// layoutTable is the one list of layouts. Adding a layout is one entry here.
var layoutTable = [...]layout{scrollLayout{}, fixedLayout{}, cascadeLayout{}}

// layoutFor resolves a config value; "" is the default layout. A scan of the
// table over zero-size layout values: no map, no allocation.
func layoutFor(o Overflow) (layout, bool) {
	if o == "" {
		o = defaultLayout
	}
	for _, l := range layoutTable {
		if l.name() == o {
			return l, true
		}
	}
	return nil, false
}

// LayoutNames lists the config values of every layout, in table order.
func LayoutNames() []string {
	names := make([]string, len(layoutTable))
	for i, l := range layoutTable {
		names[i] = string(l.name())
	}
	return names
}

// resolveLayout is the layout of an overflow value: the registry's, or the
// default for an unknown value.
func resolveLayout(o Overflow) layout {
	if l, ok := layoutFor(o); ok {
		return l
	}
	l, _ := layoutFor(defaultLayout)
	return l
}

// setOverflow is the one writer of Overflow: it resolves the layout once so
// the hot paths do not scan the table.
func (w *lanes) setOverflow(o Overflow) {
	w.Overflow = o
	w.lstateReady = false
	w.lcache, w.lcacheFor = resolveLayout(o), o
}

// layout is the workspace's layout. The cached one is used while it was
// resolved for the current Overflow; otherwise (a zero Workspace, a literal or
// a direct write to Overflow) it resolves from the table and fills the cache,
// so the two never disagree. Workspace.lay wins over both. The owner goroutine
// calls it, like every other Workspace method.
func (w *lanes) layout() layout {
	if w.lay != nil {
		return w.lay
	}
	if w.lcache == nil || w.lcacheFor != w.Overflow {
		w.lstateReady = false
		w.lcache, w.lcacheFor = resolveLayout(w.Overflow), w.Overflow
	}
	return w.lcache
}
