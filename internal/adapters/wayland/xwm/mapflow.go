package xwm

import (
	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

// ICCCM WM_STATE values.
const (
	wmStateWithdrawn uint32 = 0
	wmStateNormal    uint32 = 1
)

// _NET_WM_STATE client message actions.
const (
	netStateRemove = 0
	netStateAdd    = 1
	netStateToggle = 2
)

// beginMap starts the map flow of a managed window: every property, the
// geometry and MapWindow go out in one batch (wlroots order). Also called by
// Attach for windows mapped before it.
func (x *XWM) beginMap(s Slot) {
	rec := x.tab.rec(s)
	if rec.kind != kindManaged || rec.mapping {
		return
	}
	rec.mapping, rec.ready = true, false
	rec.hasNetName = false
	rec.pending = propGeom
	xid := rec.xid
	for idx := 0; idx < propCount; idx++ {
		x.requestProp(s, idx)
	}
	b := x.conn.Buf()
	x.expect(xproto.GetGeometry(b, xproto.Drawable(xid)), kindGeom, x.tab.tag(s), 0)
	xproto.MapWindow(b, xproto.Window(xid))
}

func (x *XWM) onMapRequest(ev *xturbo.Event) {
	if s := x.tab.lookup(uint32(xproto.MapRequestOf(ev).Window())); s != 0 {
		x.beginMap(s)
	}
}

func (x *XWM) onMapNotify(ev *xturbo.Event) {
	s := x.tab.lookup(uint32(xproto.MapNotifyOf(ev).Window()))
	if s == 0 {
		return
	}
	rec := x.tab.rec(s)
	again := rec.mapped
	rec.mapped = true
	if rec.kind == kindUnmanaged && !again {
		x.requestOverrideType(s)
	}
}

// onUnmapNotify ends the mapped life of a managed window; a later MapRequest
// starts over and re-reads the properties. Synthetic events (ICCCM unmap
// requests) are not an unmap.
func (x *XWM) onUnmapNotify(ev *xturbo.Event) {
	if ev.SendEvent {
		return
	}
	s := x.tab.lookup(uint32(xproto.UnmapNotifyOf(ev).Window()))
	if s == 0 {
		return
	}
	rec := x.tab.rec(s)
	rec.mapped = false
	if rec.kind == kindUnmanaged {
		x.overrideUnmapped(s, rec)
		return
	}
	if rec.kind != kindManaged {
		return
	}
	wasMapping := rec.mapping
	rec.ready, rec.mapping, rec.pending, rec.sized = false, false, 0, false
	rec.gen++ // replies still in flight for this map name a dead tag
	if rec.gen == 0 {
		rec.gen = 1
	}
	rec.pend.dirty = false
	rec.serial, rec.surface = 0, 0
	if wasMapping {
		xid := rec.xid
		x.setProp32Typed(xid, atomWMState, x.atoms[atomWMState], []uint32{wmStateWithdrawn, 0})
		xproto.DeleteProperty(x.conn.Buf(), xproto.Window(xid), xproto.Atom(x.atoms[atomNetWMState]))
	}
	x.dropWindow(s)
	if rec.id != 0 {
		x.host.Dissociate(s, false)
	}
}

// setProp32Typed is setProp32 with an explicit property type atom.
func (x *XWM) setProp32Typed(win uint32, prop int, typ uint32, vals []uint32) {
	x.setProp32(win, prop, xproto.Atom(typ), vals)
}

// checkReady fires Ready once every awaited reply is in.
func (x *XWM) checkReady(s Slot) {
	rec := x.tab.rec(s)
	if rec == nil || rec.pending != 0 || rec.ready || !rec.mapping || rec.kind != kindManaged || x.host == nil {
		return
	}
	rec.ready = true
	x.classify(rec)
	info := x.info(s)
	xid := rec.xid

	var state [2]uint32
	n := 0
	if rec.modal {
		state[n] = x.atoms[atomNetWMStateModal]
		n++
	}
	if rec.fullscreen {
		state[n] = x.atoms[atomNetWMStateFullscreen]
		n++
	}
	x.setProp32(xid, atomNetWMState, xproto.AtomAtom, state[:n])
	x.setProp32Typed(xid, atomWMState, x.atoms[atomWMState], []uint32{wmStateNormal, 0})
	x.addClient(xid)

	x.host.Ready(s, info)
	x.tryPair(s)
}

// info is what the host learns when a window is ready.
func (x *XWM) info(s Slot) WindowInfo {
	rec := x.tab.rec(s)
	parent := x.managedSlot(rec.transient)
	info := WindowInfo{
		PID:        int(rec.pid),
		Floating:   rec.floating,
		Parent:     parent,
		Modal:      rec.modal,
		Fullscreen: rec.fullscreen,
		Title:      rec.title,
		AppID:      rec.class,
	}
	if info.Floating || rec.sized {
		info.Width, info.Height = int(rec.w), int(rec.h)
	}
	return info
}

func (x *XWM) onGeomReply(s Slot, rec *record, r *xturbo.Reply) {
	if g, ok := xproto.ParseGetGeometryReply(r.Raw); ok {
		rec.x, rec.y, rec.w, rec.h = int32(g.X()), int32(g.Y()), uint32(g.Width()), uint32(g.Height())
	}
	rec.pending &^= propGeom
	x.checkReady(s)
}

// onClientMessage handles WL_SURFACE_SERIAL (pairing), the legacy
// WL_SURFACE_ID (counted, ignored) and _NET_WM_STATE requests.
func (x *XWM) onClientMessage(ev *xturbo.Event) {
	e := xproto.ClientMessageOf(ev)
	if e.Format() != 32 {
		return
	}
	typ := uint32(e.Type())
	switch typ {
	case x.atoms[atomWLSurfaceSerial]:
		s := x.tab.lookup(uint32(e.Window()))
		if s == 0 {
			return
		}
		rec := x.tab.rec(s)
		if rec.serial != 0 {
			x.log.Debug().Uint32("xid", rec.xid).Msg("duplicate WL_SURFACE_SERIAL ignored")
			return
		}
		d := e.Data().Data32()
		rec.serial = uint64(d[1])<<32 | uint64(d[0])
		x.tryPair(s)
	case x.atoms[atomWLSurfaceID]:
		x.stats.LegacyID++
		if x.stats.LegacyID == 1 {
			x.log.Warn().Msg("WL_SURFACE_ID received: Xwayland did not bind xwayland_shell_v1, window ignored")
		}
	case x.atoms[atomNetWMState]:
		x.onNetWMState(e)
	case x.atoms[atomWMChangeState]:
		// Iconify requests are ignored: windows stay mapped.
	}
}

// onNetWMState forwards fullscreen requests to the host and records them; the
// property itself is written when core answers with a configure.
func (x *XWM) onNetWMState(e xproto.ClientMessageEvent) {
	s := x.tab.lookup(uint32(e.Window()))
	if s == 0 {
		return
	}
	rec := x.tab.rec(s)
	if rec.kind != kindManaged {
		return
	}
	d := e.Data().Data32()
	apply := func(cur bool) bool {
		switch d[0] {
		case netStateRemove:
			return false
		case netStateAdd:
			return true
		case netStateToggle:
			return !cur
		}
		return cur
	}
	for _, a := range d[1:3] {
		switch {
		case a == 0:
		case a == x.atoms[atomNetWMStateModal]:
			rec.modal = apply(rec.modal)
		case a == x.atoms[atomNetWMStateFullscreen]:
			on := apply(rec.fullscreen)
			if on == rec.fullscreen {
				continue
			}
			rec.fullscreen = on
			if rec.ready {
				x.host.Fullscreen(s, on)
				rec = x.tab.rec(s)
			}
		}
	}
}

// onReparentNotify keeps the table to root's direct children: a window moved
// away is forgotten, one moved into root is treated as newly created.
func (x *XWM) onReparentNotify(ev *xturbo.Event) {
	e := xproto.ReparentNotifyOf(ev)
	xid := uint32(e.Window())
	if xid == x.wmWin {
		return
	}
	s := x.tab.lookup(xid)
	if uint32(e.Parent()) != x.root {
		if s != 0 {
			x.forgetWindow(s)
		}
		return
	}
	if s != 0 {
		return
	}
	s = x.insert(xid)
	if s == 0 {
		return
	}
	rec := x.tab.rec(s)
	rec.x, rec.y = int32(e.X()), int32(e.Y())
	if e.OverrideRedirect() {
		rec.kind = kindUnmanaged
		x.stats.UnmanagedSeen++
		return
	}
	if x.host != nil {
		x.adopt(s)
	}
}

// tryPair links the window to the wl_surface carrying its serial, once both
// are known and the window is ready (the host maps on the surface's first
// buffer, so it must know the window's info by then). Unmanaged windows keep
// the surface but are never associated.
func (x *XWM) tryPair(s Slot) {
	rec := x.tab.rec(s)
	if rec.serial == 0 || rec.surface != 0 {
		return
	}
	if rec.kind == kindManaged && !rec.ready {
		return
	}
	for i := 0; i < x.unpairedN; i++ {
		if x.unpaired[i].serial != rec.serial {
			continue
		}
		rec.surface = x.unpaired[i].surface
		x.unpairedN--
		x.unpaired[i] = x.unpaired[x.unpairedN]
		if rec.kind == kindManaged {
			x.log.Debug().Uint32("xid", rec.xid).Uint64("surface", rec.surface).Msg("x11 window paired")
			x.host.Associate(s, rec.surface)
		} else {
			x.overrideMapped(s, rec)
		}
		return
	}
}

// SetSerial records the serial of a wl_surface (xwayland_surface_v1.set_serial)
// and pairs it with the window announcing the same serial.
func (x *XWM) SetSerial(surfaceID uint64, serial uint64) {
	if serial == 0 || x.closed {
		return
	}
	for i := 0; i < x.unpairedN; i++ {
		if x.unpaired[i].serial == serial {
			x.unpaired[i].surface = surfaceID
			x.pairSerial(serial)
			return
		}
	}
	if x.unpairedN == unpairedCap {
		copy(x.unpaired[:], x.unpaired[1:])
		x.unpairedN--
		if !x.warnedUnpaired {
			x.warnedUnpaired = true
			x.log.Warn().Msg("too many unpaired surfaces, dropping the oldest")
		}
	}
	x.unpaired[x.unpairedN] = struct{ serial, surface uint64 }{serial, surfaceID}
	x.unpairedN++
	x.pairSerial(serial)
}

// pairSerial offers a newly known surface serial to the windows.
func (x *XWM) pairSerial(serial uint64) {
	for i := range x.tab.recs {
		if r := &x.tab.recs[i]; r.kind != kindFree && r.serial == serial && r.surface == 0 {
			x.tryPair(Slot(i + 1))
			break
		}
	}
	x.wake()
}

// SurfaceGone forgets a destroyed wl_surface.
func (x *XWM) SurfaceGone(surfaceID uint64) {
	for i := 0; i < x.unpairedN; i++ {
		if x.unpaired[i].surface == surfaceID {
			x.unpairedN--
			x.unpaired[i] = x.unpaired[x.unpairedN]
			break
		}
	}
	for i := range x.tab.recs {
		if r := &x.tab.recs[i]; r.kind != kindFree && r.surface == surfaceID {
			r.surface = 0
			if r.kind == kindUnmanaged && r.ovPaired {
				r.ovPaired = false
				x.host.OverrideGone(Slot(i + 1))
			}
		}
	}
	x.wake()
}
