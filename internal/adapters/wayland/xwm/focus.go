package xwm

import (
	"encoding/binary"
	"slices"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

// Bits of XWM.listsDirty: the client lists to rewrite at flush.
const (
	listClients uint8 = 1 << iota
	listStacking
)

// onFocusIn denies a focus change the XWM did not make: Wayland is the truth.
// The active window's focus is re-issued once at the next flush.
func (x *XWM) onFocusIn(ev *xturbo.Event) {
	e := xproto.FocusInOf(ev)
	if e.Detail() == xproto.NotifyDetailPointer {
		return
	}
	s := x.tab.lookup(uint32(e.Event()))
	if s == 0 || s == x.active {
		return
	}
	x.refocus = true
	x.wake()
}

// Focus gives keyboard focus to s; 0 means a native Wayland window or none.
// Focus never uses PointerRoot: without an X window to focus it is None.
func (x *XWM) Focus(s Slot) {
	if x.closed {
		return
	}
	r := x.managedRec(s)
	if r == nil {
		s = 0
	}
	if s == x.active {
		return
	}
	if old := x.managedRec(x.active); old != nil {
		old.pend.activated = false
		x.markDirty(x.active, old)
	}
	x.active = s
	if r == nil {
		x.issueFocus(0, nil)
		x.setActive(0)
		x.wake()
		return
	}
	r.pend.activated = true
	x.markDirty(s, r)
	x.issueFocus(s, r)
	x.setActive(r.xid)
	x.raise(r)
	x.wake()
}

// issueFocus applies the ICCCM input model of r: Passive and Locally active
// windows get SetInputFocus, Locally and Globally active ones WM_TAKE_FOCUS,
// No-input windows nothing. A nil r clears the X focus.
func (x *XWM) issueFocus(s Slot, r *record) {
	b := x.conn.Buf()
	if r == nil {
		xproto.SetInputFocus(b, xproto.InputFocusNone, xproto.WindowNone, 0)
		return
	}
	if r.input {
		x.expect(xproto.SetInputFocus(b, xproto.InputFocusNone, xproto.Window(r.xid), 0), kindFocus, x.tab.tag(s), 0)
	}
	if r.takeFocus {
		x.sendProtocol(r.xid, atomWMTakeFocus)
	}
}

// sendProtocol sends a WM_PROTOCOLS client message [proto, CurrentTime].
func (x *XWM) sendProtocol(xid uint32, proto int) {
	var raw [32]byte
	raw[0], raw[1] = 33, 32 // ClientMessage, format 32
	binary.LittleEndian.PutUint32(raw[4:], xid)
	binary.LittleEndian.PutUint32(raw[8:], x.atoms[atomWMProtocols])
	binary.LittleEndian.PutUint32(raw[12:], x.atoms[proto])
	xproto.SendEvent(x.conn.Buf(), false, xproto.Window(xid), xproto.EventMaskNoEvent, &raw)
}

// setActive writes _NET_ACTIVE_WINDOW on the root.
func (x *XWM) setActive(xid uint32) {
	v := [1]uint32{xid}
	x.setProp32(x.root, atomNetActiveWindow, xproto.AtomWindow, v[:])
}

// raise restacks r above its siblings and at the top of the stacking list.
func (x *XWM) raise(r *record) {
	var v xproto.ConfigureWindowValues
	v.SetStackMode(xproto.StackModeAbove)
	xproto.ConfigureWindow(x.conn.Buf(), xproto.Window(r.xid), &v)
	if i := slices.Index(x.stacking, r.xid); i >= 0 {
		x.stacking = append(x.stacking[:i], x.stacking[i+1:]...)
		x.stacking = append(x.stacking, r.xid)
		x.listsDirty |= listStacking
	}
}

// CloseWindow asks s to close (WM_DELETE_WINDOW), or kills its client.
func (x *XWM) CloseWindow(s Slot) {
	r := x.managedRec(s)
	if r == nil {
		return
	}
	if r.deleteWindow {
		x.sendProtocol(r.xid, atomWMDeleteWindow)
	} else {
		xproto.KillClient(x.conn.Buf(), r.xid)
	}
	x.wake()
}

// addClient puts a ready window at the end of both client lists.
func (x *XWM) addClient(xid uint32) {
	if !slices.Contains(x.clients, xid) {
		x.clients = append(x.clients, xid)
		x.listsDirty |= listClients
	}
	if !slices.Contains(x.stacking, xid) {
		x.stacking = append(x.stacking, xid)
		x.listsDirty |= listStacking
	}
	x.wake()
}

// removeClient drops an unmapped or destroyed window from both client lists.
func (x *XWM) removeClient(xid uint32) {
	if i := slices.Index(x.clients, xid); i >= 0 {
		x.clients = slices.Delete(x.clients, i, i+1)
		x.listsDirty |= listClients
	}
	if i := slices.Index(x.stacking, xid); i >= 0 {
		x.stacking = slices.Delete(x.stacking, i, i+1)
		x.listsDirty |= listStacking
	}
	x.wake()
}

// dropWindow takes s out of the client lists, the focus and the configure
// queue when it stops being mapped (unmap, destroy). It is idempotent.
func (x *XWM) dropWindow(s Slot) {
	r := x.tab.rec(s)
	if r == nil || r.kind != kindManaged {
		return
	}
	x.removeClient(r.xid)
	r.pend.dirty = false // the queued slot is skipped at flush
	// A remap starts from a clean _NET_WM_STATE, so Focus writes FOCUSED again.
	r.pend.activated, r.sent.activated = false, false
	r.sent.fullscreen, r.stateSent = false, 0
	if x.active == s {
		x.active = 0
		x.refocus = false
		x.setActive(0)
	}
}

// forgetWindow ends the life of s (destroy, reparent away): it is dropped,
// the host is told if it knew the window, and the record is freed.
func (x *XWM) forgetWindow(s Slot) {
	r := x.tab.rec(s)
	if r == nil || r.kind == kindFree {
		return
	}
	x.dropWindow(s)
	if r.kind == kindManaged && r.id != 0 && x.host != nil {
		r.id = 0
		x.host.Dissociate(s, true)
		if x.active == s { // the host re-focused the window while dissociating it
			x.active, x.refocus = 0, false
			x.issueFocus(0, nil)
			x.setActive(0)
			x.wake()
		}
	}
	x.tab.remove(s)
}

// flushFocus rewrites the client lists that changed and re-issues a denied
// focus, once per display iteration.
func (x *XWM) flushFocus() {
	if x.listsDirty&listClients != 0 {
		x.setProp32(x.root, atomNetClientList, xproto.AtomWindow, x.clients)
	}
	if x.listsDirty&listStacking != 0 {
		x.setProp32(x.root, atomNetClientListStacking, xproto.AtomWindow, x.stacking)
	}
	x.listsDirty = 0
	if x.refocus {
		x.refocus = false
		x.issueFocus(x.active, x.managedRec(x.active))
	}
}
