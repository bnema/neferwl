package core

import "slices"

// state is the layout's per-workspace data, built on first use. Stateless
// layouts keep nil.
func (w *Workspace) state() layoutState {
	if w.lcacheFor != w.Overflow {
		w.layout()
	}
	if !w.lstateReady {
		if w.lstate == nil {
			w.lstate = w.layout().newState()
		}
		w.lstateReady = true
	}
	return w.lstate
}

// newKey issues a workspace-local column identity.
func (w *Workspace) newKey() uint64 {
	w.colSeq++
	return w.colSeq
}

// ensureKeys gives every keyless column an identity.
func (w *Workspace) ensureKeys() {
	for _, c := range w.Columns {
		w.colSeq = max(w.colSeq, c.key)
	}
	for i := range w.Columns {
		if w.Columns[i].key == 0 {
			w.Columns[i].key = w.newKey()
		}
	}
}

// columnInserted follows Columns[at] being added: the column gets an
// identity of this workspace, and the layout state receives how it was
// inserted and what the old workspace's state let travel with it.
func (w *Workspace) columnInserted(at int, kind insertKind) {
	w.ensureKeys()
	// Incoming columns may carry an identity from a different workspace.
	w.Columns[at].key = w.newKey()
	// A stateless workspace keeps the carry on the column for the next
	// takeColumn to pass on; a state keeps only what it did not consume.
	if s := w.state(); s != nil {
		w.Columns[at].carry = s.inserted(w, at, kind, w.Columns[at].carry)
	}
}

// settleLayout runs before scroll() measures: keys exist, the layout state
// refreshes, stale manual widths go.
func (w *Workspace) settleLayout() {
	s := w.state()
	w.ensureKeys()
	if s != nil {
		s.settle(w)
	} else {
		w.dropStaleSpans()
	}
}

// resetLayoutState forgets the layout state: on a layout switch, the next
// use builds it from the column order. Carries parked on columns by a
// stateless workspace go with it.
func (w *Workspace) resetLayoutState() {
	w.lstate = nil
	w.lstateReady = false
	for i := range w.Columns {
		w.Columns[i].carry = nil
	}
}

// deleteColumn removes column i and returns what travels with it to another
// workspace: what the layout state hands out, else the carry a stateless
// workspace kept on the column.
func (w *Workspace) deleteColumn(i int) (carry any) {
	// A removed member cannot be detected by counting surviving stamp members.
	w.clearSpans(i)
	col := w.Columns[i]
	s := w.state()
	w.Columns = slices.Delete(w.Columns, i, i+1)
	if s != nil {
		carry = s.removed(i, col)
	}
	if carry == nil {
		carry = col.carry
	}
	return carry
}
