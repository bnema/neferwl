package xwm

import (
	"math"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
)

// maxClientSize bounds the size a floating window may ask for.
const maxClientSize = 16384

// _NET_WM_STATE bits tracked in record.stateSent.
const (
	stateFullscreen uint8 = 1 << iota
	stateFocused
)

// size is the size to send: the requested one, else the last known.
func (r *record) size() (w, h uint32) {
	w, h = r.pend.w, r.pend.h
	if w == 0 {
		w = r.w
	}
	if h == 0 {
		h = r.h
	}
	return w, h
}

// differs reports whether the pending configure is not what the X server has.
func (r *record) differs() bool {
	w, h := r.size()
	p, s := &r.pend, &r.sent
	return p.x != s.x || p.y != s.y || w != s.w || h != s.h ||
		p.fullscreen != s.fullscreen || p.activated != s.activated
}

// markDirty queues s for the next flush when its pending configure differs.
func (x *XWM) markDirty(s Slot, r *record) {
	if r.pend.dirty || !r.differs() {
		return
	}
	if len(x.dirty) == cap(x.dirty) {
		// Stale entries of freed and reused slots filled the queue: send
		// what is queued rather than grow it. r stays valid, nothing here
		// grows the table.
		x.flushConfigures()
	}
	r.pend.dirty = true
	x.dirty = append(x.dirty, s)
}

// managedRec returns the record of a live managed window.
func (x *XWM) managedRec(s Slot) *record {
	if x.closed {
		return nil
	}
	if r := x.tab.rec(s); r != nil && r.kind == kindManaged {
		return r
	}
	return nil
}

// Configure records the size and state core wants for s; sent at flush. A
// zero size keeps the current one.
func (x *XWM) Configure(s Slot, c ports.ConfigureWindow) {
	r := x.managedRec(s)
	if r == nil {
		return
	}
	if c.Width > 0 {
		r.pend.w = uint32(min(c.Width, maxClientSize))
	}
	if c.Height > 0 {
		r.pend.h = uint32(min(c.Height, maxClientSize))
	}
	r.pend.fullscreen, r.pend.activated = c.Fullscreen, c.Activated
	x.markDirty(s, r)
	x.wake()
}

// Place records the layout position of the client rectangle of s.
func (x *XWM) Place(s Slot, px, py int) {
	r := x.managedRec(s)
	if r == nil {
		return
	}
	r.lx, r.ly, r.placed = int32(px), int32(py), true
	r.pend.x, r.pend.y = x.toX(r)
	x.markDirty(s, r)
	x.wake()
}

// toX converts the layout position of r to X coordinates.
func (x *XWM) toX(r *record) (px, py int32) {
	ix, iy := x.space.ToX(int(r.lx), int(r.ly))
	return int32(ix), int32(iy)
}

// SetLayout installs the output layout that X coordinates derive from. When it
// changes, placed managed windows are repositioned and mapped override windows
// are re-reported in layout coordinates. Display goroutine only.
func (x *XWM) SetLayout(l ports.Layout) {
	if x.closed {
		return
	}
	x.LayoutChanged(x.space.Set(l))
}

// UseSpace makes sp the coordinate mapping, owned by the host, which calls
// Set on it and then LayoutChanged. Call before Attach.
func (x *XWM) UseSpace(sp *Space) { x.space = sp }

// LayoutChanged repositions windows after the shared Space was set; changed
// is Set's result. Display goroutine only.
func (x *XWM) LayoutChanged(changed bool) {
	if x.closed {
		return
	}
	if n := x.space.Dropped(); n > 0 && !x.warnedSpace {
		x.warnedSpace = true
		x.log.Warn().Int("dropped", n).Int("max", maxRegions).Msg("more outputs than the X11 layout holds, extra outputs ignored")
	}
	if !changed {
		return
	}
	for i := range x.tab.recs {
		r, s := &x.tab.recs[i], Slot(i+1)
		switch {
		case r.kind == kindManaged && r.placed:
			r.pend.x, r.pend.y = x.toX(r)
			x.markDirty(s, r)
		case r.kind == kindUnmanaged && r.ovPaired:
			x.queueOverride(s, r)
		}
	}
	x.wake()
}

// flushConfigures sends the pending configures, once per display iteration,
// then what focus left to do.
func (x *XWM) flushConfigures() {
	for _, s := range x.dirty {
		if r := x.tab.rec(s); r != nil && r.kind == kindManaged && r.pend.dirty {
			x.sendPending(s, r)
		}
	}
	x.dirty = x.dirty[:0]
	x.flushOverrides()
	x.flushFocus()
}

// sendPending writes the geometry (with the synthetic ConfigureNotify ICCCM
// 4.1.5 asks for, since Xwayland may not move the window) and the
// _NET_WM_STATE bits that changed.
func (x *XWM) sendPending(s Slot, r *record) {
	r.pend.dirty = false
	p := &r.pend
	w, h := r.size()
	if w > 0 && h > 0 && (p.x != r.sent.x || p.y != r.sent.y || w != r.sent.w || h != r.sent.h) {
		var v xproto.ConfigureWindowValues
		v.SetX(p.x).SetY(p.y).SetWidth(w).SetHeight(h).SetBorderWidth(0)
		x.expect(xproto.ConfigureWindow(x.conn.Buf(), xproto.Window(r.xid), &v), kindConfigure, x.tab.tag(s), 0)
		r.x, r.y, r.w, r.h = p.x, p.y, w, h
		x.sendConfigureNotify(r)
		r.sent.x, r.sent.y, r.sent.w, r.sent.h = p.x, p.y, w, h
	}
	var bits uint8
	if p.fullscreen {
		bits |= stateFullscreen
	}
	if p.activated {
		bits |= stateFocused
	}
	r.sent.fullscreen, r.sent.activated = p.fullscreen, p.activated
	r.fullscreen = p.fullscreen // ours now: the property is not read back
	if bits != r.stateSent {
		r.stateSent = bits
		x.writeState(r)
	}
}

// writeState rewrites _NET_WM_STATE of r from its bits.
func (x *XWM) writeState(r *record) {
	var list [3]uint32
	n := 0
	if r.modal {
		list[n] = x.atoms[atomNetWMStateModal]
		n++
	}
	if r.stateSent&stateFullscreen != 0 {
		list[n] = x.atoms[atomNetWMStateFullscreen]
		n++
	}
	if r.stateSent&stateFocused != 0 {
		list[n] = x.atoms[atomNetWMStateFocused]
		n++
	}
	x.setProp32(r.xid, atomNetWMState, xproto.AtomAtom, list[:n])
}

// sendConfigureNotify tells the client its geometry as the XWM sees it.
func (x *XWM) sendConfigureNotify(r *record) {
	var raw [32]byte
	d := xproto.ConfigureNotifyEventData{
		Event: xproto.Window(r.xid), Window: xproto.Window(r.xid),
		X: int16(min(max(r.x, math.MinInt16), math.MaxInt16)), Y: int16(min(max(r.y, math.MinInt16), math.MaxInt16)),
		Width: uint16(min(r.w, math.MaxUint16)), Height: uint16(min(r.h, math.MaxUint16)),
	}
	d.Put(&raw)
	xproto.SendEvent(x.conn.Buf(), false, xproto.Window(r.xid), xproto.EventMaskStructureNotify, &raw)
}

// onConfigureRequest answers a client resizing itself. Tiled windows are told
// their current geometry; floating ones get the size they ask for; unmanaged
// and unknown windows are configured as requested.
func (x *XWM) onConfigureRequest(ev *xturbo.Event) {
	e := xproto.ConfigureRequestOf(ev)
	xid := uint32(e.Window())
	mask := e.ValueMask()
	s := x.tab.lookup(xid)
	r := x.managedRec(s)
	if r == nil {
		var v xproto.ConfigureWindowValues
		if mask&xproto.ConfigWindowX != 0 {
			v.SetX(int32(e.X()))
		}
		if mask&xproto.ConfigWindowY != 0 {
			v.SetY(int32(e.Y()))
		}
		if mask&xproto.ConfigWindowWidth != 0 {
			v.SetWidth(uint32(e.Width()))
		}
		if mask&xproto.ConfigWindowHeight != 0 {
			v.SetHeight(uint32(e.Height()))
		}
		if mask&xproto.ConfigWindowBorderWidth != 0 {
			v.SetBorderWidth(uint32(e.BorderWidth()))
		}
		if mask&xproto.ConfigWindowSibling != 0 {
			v.SetSibling(e.Sibling())
		}
		if mask&xproto.ConfigWindowStackMode != 0 {
			v.SetStackMode(e.StackMode())
		}
		xproto.ConfigureWindow(x.conn.Buf(), xproto.Window(xid), &v)
		return
	}
	if !r.ready {
		x.configureBeforeReady(s, r, e)
		return
	}
	if r.floating && mask&(xproto.ConfigWindowWidth|xproto.ConfigWindowHeight) != 0 {
		w, h := r.w, r.h
		if mask&xproto.ConfigWindowWidth != 0 {
			w = uint32(min(max(int(e.Width()), 1), maxClientSize))
		}
		if mask&xproto.ConfigWindowHeight != 0 {
			h = uint32(min(max(int(e.Height()), 1), maxClientSize))
		}
		if w > 0 && h > 0 {
			var v xproto.ConfigureWindowValues
			v.SetWidth(w).SetHeight(h)
			x.expect(xproto.ConfigureWindow(x.conn.Buf(), xproto.Window(xid), &v), kindConfigure, x.tab.tag(s), 0)
			r.w, r.h = w, h
			// The size is the client's own: core learns it from the commit
			// and Configure only resends it when core wants another.
			r.pend.w, r.pend.h = w, h
			r.sent.w, r.sent.h = w, h
		}
	}
	x.sendConfigureNotify(r)
}

// configureBeforeReady honours the geometry a window asks for before it is
// ready (XMoveResize before XMapWindow), floating or not: core only sees the
// window at Ready and the client must find the geometry it set.
func (x *XWM) configureBeforeReady(s Slot, r *record, e xproto.ConfigureRequestEvent) {
	mask := e.ValueMask()
	var v xproto.ConfigureWindowValues
	if mask&xproto.ConfigWindowX != 0 {
		r.x = int32(e.X())
		v.SetX(r.x)
	}
	if mask&xproto.ConfigWindowY != 0 {
		r.y = int32(e.Y())
		v.SetY(r.y)
	}
	if mask&xproto.ConfigWindowWidth != 0 {
		r.w, r.sized = uint32(min(max(int(e.Width()), 1), maxClientSize)), true
		v.SetWidth(r.w)
	}
	if mask&xproto.ConfigWindowHeight != 0 {
		r.h, r.sized = uint32(min(max(int(e.Height()), 1), maxClientSize)), true
		v.SetHeight(r.h)
	}
	if mask&(xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight) != 0 {
		x.expect(xproto.ConfigureWindow(x.conn.Buf(), xproto.Window(r.xid), &v), kindConfigure, x.tab.tag(s), 0)
	}
	x.sendConfigureNotify(r)
}

// onConfigureNotify keeps the known geometry of every top-level window.
func (x *XWM) onConfigureNotify(ev *xturbo.Event) {
	e := xproto.ConfigureNotifyOf(ev)
	if e.Event() != xproto.Window(x.root) {
		return
	}
	if r := x.tab.rec(x.tab.lookup(uint32(e.Window()))); r != nil && r.kind != kindFree {
		r.x, r.y, r.w, r.h = int32(e.X()), int32(e.Y()), uint32(e.Width()), uint32(e.Height())
		if r.kind == kindUnmanaged && r.ovPaired {
			x.queueOverride(x.tab.lookup(r.xid), r)
		}
	}
}
