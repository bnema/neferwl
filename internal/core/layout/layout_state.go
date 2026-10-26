package layout

import "slices"

// state is the layout's per-workspace data, built on first use. Stateless
// layouts keep nil.
func (w *Lanes) state() State {
	if w.lcacheFor != w.Overflow {
		w.Engine()
	}
	if !w.lstateReady {
		if w.lstate == nil {
			w.lstate = w.Engine().NewState()
		}
		w.lstateReady = true
	}
	return w.lstate
}

// newKey issues a workspace-local column identity.
func (w *Lanes) newKey() uint64 {
	w.colSeq++
	return w.colSeq
}

// ensureKeys gives every keyless column an identity.
func (w *Lanes) ensureKeys() {
	for _, c := range w.Columns {
		w.colSeq = max(w.colSeq, c.Key)
	}
	for i := range w.Columns {
		if w.Columns[i].Key == 0 {
			w.Columns[i].Key = w.newKey()
		}
	}
}

// ColumnInserted follows Columns[at] being added: the column gets an
// identity of this workspace, and the layout state receives how it was
// inserted and what the old workspace's state let travel with it.
func (w *Lanes) ColumnInserted(at int, kind InsertKind) {
	w.ensureKeys()
	// Incoming columns may carry an identity from a different workspace.
	w.Columns[at].Key = w.newKey()
	// A stateless workspace keeps the carry on the column for the next
	// takeColumn to pass on; a state keeps only what it did not consume.
	if s := w.state(); s != nil {
		w.Columns[at].Carry = s.Inserted(w, at, kind, w.Columns[at].Carry)
	}
}

// SettleLayout runs before scroll() measures: keys exist, the layout state
// refreshes, stale manual widths go.
func (w *Lanes) SettleLayout() {
	s := w.state()
	w.ensureKeys()
	if s != nil {
		s.Settle(w)
	} else {
		w.DropStaleSpans()
	}
}

// ResetLayoutState forgets the layout state: on a layout switch, the next
// use builds it from the column order. Carries parked on columns by a
// stateless workspace go with it.
func (w *Lanes) ResetLayoutState() {
	w.lstate = nil
	w.lstateReady = false
	for i := range w.Columns {
		w.Columns[i].Carry = nil
	}
}

// DeleteColumn removes column i and returns what travels with it to another
// workspace: what the layout state hands out, else the carry a stateless
// workspace kept on the column.
func (w *Lanes) DeleteColumn(i int) (carry any) {
	// A removed member cannot be detected by counting surviving stamp members.
	w.ClearSpans(i)
	col := w.Columns[i]
	s := w.state()
	w.Columns = slices.Delete(w.Columns, i, i+1)
	if s != nil {
		carry = s.Removed(i, col)
	}
	if carry == nil {
		carry = col.Carry
	}
	return carry
}

// DropState forgets positional layout state on a copy without changing columns
// or their carries. The next snapshot initializes state for the new order.
func (w *Lanes) DropState() {
	w.lstate = nil
	w.lstateReady = false
}

// NewStamp issues an identity for a complete lane's manual widths.
func (w *Lanes) NewStamp() uint64 {
	w.spanSeq++
	return w.spanSeq
}
