package xwm

import (
	"bnema.dev/xturbo"
	"bnema.dev/xturbo/composite"
	"bnema.dev/xturbo/res"
	"bnema.dev/xturbo/xfixes"
	"bnema.dev/xturbo/xproto"
)

// Reply kinds registered with Expect. The first argument is the window tag
// (slot | generation<<16, see table.tag), the second a property index.
const (
	kindAttr uint16 = 16 + iota
	kindProp
	kindGeom
	kindClientIds
	kindConfigure
	kindFocus
	kindRootMask
	kindVersion
	kindTree
	kindOverrideType
	kindSelTargets
	kindSelData
	kindSelOut
	kindSelOwner
	kindPictFormats
)

// onEvent dispatches an X event. Before Attach there is no host: only the
// table is maintained, and Attach replays the result.
func (x *XWM) onEvent(ev *xturbo.Event) {
	x.stats.Events++
	if x.closed {
		return
	}
	switch ev.Kind {
	case xproto.KindPropertyNotify:
		x.noteTime(uint32(xproto.PropertyNotifyOf(ev).Time()))
	case xproto.KindSelectionRequest:
		x.noteTime(uint32(xproto.SelectionRequestOf(ev).Time()))
	}
	switch ev.Kind {
	case xproto.KindCreateNotify:
		x.onCreateNotify(ev)
	case xproto.KindDestroyNotify:
		x.onDestroyNotify(ev)
	case xproto.KindReparentNotify:
		x.onReparentNotify(ev)
	}
	if ev.Kind == xproto.KindSelectionClear {
		x.onXSettingsClear(ev)
	}
	if x.host == nil {
		x.onEventDetached(ev)
		return
	}
	switch ev.Kind {
	case xproto.KindMapRequest:
		x.onMapRequest(ev)
	case xproto.KindMapNotify:
		x.onMapNotify(ev)
	case xproto.KindUnmapNotify:
		x.onUnmapNotify(ev)
	case xproto.KindConfigureRequest:
		x.onConfigureRequest(ev)
	case xproto.KindConfigureNotify:
		x.onConfigureNotify(ev)
	case xproto.KindPropertyNotify:
		x.onPropertyNotify(ev)
	case xproto.KindClientMessage:
		x.onClientMessage(ev)
	case xproto.KindFocusIn:
		x.onFocusIn(ev)
	default:
		x.onSelectionEvent(ev)
	}
}

// onEventDetached keeps what Attach replays: windows asked to map, and those
// withdrawn again, so Attach skips them; geometry requests are answered.
func (x *XWM) onEventDetached(ev *xturbo.Event) {
	switch ev.Kind {
	case xproto.KindMapRequest:
		if s := x.tab.lookup(uint32(xproto.MapRequestOf(ev).Window())); s != 0 {
			x.tab.rec(s).mapping = true
		}
	case xproto.KindUnmapNotify:
		if ev.SendEvent {
			return
		}
		if s := x.tab.lookup(uint32(xproto.UnmapNotifyOf(ev).Window())); s != 0 {
			r := x.tab.rec(s)
			r.mapped, r.mapping = false, false
		}
	case xproto.KindConfigureRequest:
		x.onConfigureRequest(ev)
	}
}

func (x *XWM) onReply(r *xturbo.Reply) {
	x.stats.Replies++
	if x.closed {
		return
	}
	switch r.Kind {
	case kindVersion:
		x.onVersionReply(r)
		return
	case kindTree:
		x.onTreeReply(r)
		return
	case kindSelTargets:
		x.onTargetsReply(r)
		return
	case kindSelData:
		x.onSelDataReply(r)
		return
	case kindSelOwner:
		x.onSelOwnerReply(r)
		return
	case kindPictFormats:
		x.onPictFormatsReply(r)
		return
	}
	s, rec := x.tab.live(r.Args[0])
	if rec == nil {
		x.stats.Stale++
		return
	}
	switch r.Kind {
	case kindAttr:
		x.onAttrReply(rec, r)
	case kindProp:
		x.onPropReply(s, rec, r)
	case kindGeom:
		x.onGeomReply(s, rec, r)
	case kindClientIds:
		x.onClientIdsReply(s, rec, r)
	case kindOverrideType:
		x.onOverrideType(s, rec, r)
	}
}

// onError never tears the XWM down: an error names a request, not the
// connection. Sticky Buf errors surface through Flush.
func (x *XWM) onError(e *xturbo.XError) {
	x.stats.Errors++
	if e.Kind == 0 {
		x.stats.Unattributed++
		x.log.Debug().Uint8("code", e.Code).Uint8("major", e.Major).Uint16("minor", e.Minor).
			Uint64("seq", uint64(e.Seq)).Msg("unattributed X error")
		return
	}
	switch e.Kind {
	case kindSelTargets, kindSelData, kindSelOut:
		x.onSelError(e)
	case kindRootMask:
		if e.Code == xproto.BadAccess {
			x.bootErr = ErrWMExists
		}
	case kindProp, kindGeom, kindClientIds, kindConfigure, kindFocus, kindOverrideType:
		// Usually BadWindow: the window died between request and reply and
		// DestroyNotify follows. Whatever the code, the awaited reply will
		// never come, so the map must not wait for it.
		s, rec := x.tab.live(e.Args[0])
		if rec == nil {
			x.stats.Stale++
			return
		}
		switch e.Kind {
		case kindProp:
			rec.pending &^= 1 << e.Args[1]
		case kindGeom:
			rec.pending &^= propGeom
		case kindClientIds:
			rec.pending &^= propClientIDs
		case kindOverrideType:
			if e.Code != xproto.BadWindow { // typed unknown: the window still shows
				x.overrideReady(s)
			}
			return
		default:
			return
		}
		if e.Code != xproto.BadWindow && e.Code != xproto.BadDrawable { // a dead window is never announced
			x.checkReady(s)
		}
	}
}

// ownWindow reports whether xid is one of the XWM's own helper windows.
func (x *XWM) ownWindow(xid uint32) bool {
	return xid != 0 && (xid == x.wmWin || xid == x.selWin || xid == x.xs.win)
}

// onCreateNotify records a new top-level window. Override-redirect windows
// are kept (their geometry and serial matter later) but never reach the host.
func (x *XWM) onCreateNotify(ev *xturbo.Event) {
	e := xproto.CreateNotifyOf(ev)
	xid := uint32(e.Window())
	if x.ownWindow(xid) || x.tab.lookup(xid) != 0 {
		return
	}
	s := x.insert(xid)
	if s == 0 {
		return
	}
	r := x.tab.rec(s)
	override := e.OverrideRedirect()
	r.x, r.y, r.w, r.h = int32(e.X()), int32(e.Y()), uint32(e.Width()), uint32(e.Height())
	if override {
		r.kind = kindUnmanaged
		x.stats.UnmanagedSeen++
		return
	}
	if x.host != nil {
		x.adopt(s)
	}
}

// adopt announces a managed window to the host and starts watching it.
func (x *XWM) adopt(s Slot) {
	r := x.tab.rec(s)
	r.id = x.host.Created(s, r.xid)
	var v xproto.ChangeWindowAttributesValues
	v.SetEventMask(xproto.EventMaskPropertyChange | xproto.EventMaskFocusChange)
	x.expect(xproto.ChangeWindowAttributes(x.conn.Buf(), xproto.Window(r.xid), &v), kindAttr, x.tab.tag(s), 0)
	x.log.Debug().Uint32("xid", r.xid).Uint32("slot", uint32(s)).Msg("x11 window created")
}

func (x *XWM) onDestroyNotify(ev *xturbo.Event) {
	xid := uint32(xproto.DestroyNotifyOf(ev).Window())
	s := x.tab.lookup(xid)
	if s == 0 {
		return
	}
	x.forgetWindow(s)
}

// onTreeReply (boot): record every existing child and ask for its attributes.
func (x *XWM) onTreeReply(r *xturbo.Reply) {
	q, ok := xproto.ParseQueryTreeReply(r.Raw)
	if !ok {
		return
	}
	children := q.Children()
	for i := 0; i < children.Len(); i++ {
		xid := uint32(children.At(i))
		if x.ownWindow(xid) || x.tab.lookup(xid) != 0 {
			continue
		}
		s := x.insert(xid)
		if s == 0 {
			return
		}
		x.expect(xproto.GetWindowAttributes(x.conn.Buf(), xproto.Window(xid)), kindAttr, x.tab.tag(s), 0)
	}
}

// onAttrReply (boot): classify an existing window; viewable ones go through
// the map flow once the host is attached.
func (x *XWM) onAttrReply(rec *record, r *xturbo.Reply) {
	a, ok := xproto.ParseGetWindowAttributesReply(r.Raw)
	if !ok || x.host != nil {
		return
	}
	if a.OverrideRedirect() {
		rec.kind = kindUnmanaged
		rec.mapped = a.MapState() == xproto.MapStateViewable
		x.stats.UnmanagedSeen++
		return
	}
	if a.MapState() == xproto.MapStateViewable {
		rec.mapped, rec.mapping = true, true
	}
}

func (x *XWM) onVersionReply(r *xturbo.Reply) {
	switch r.Args[0] {
	case versionComposite:
		if v, ok := composite.ParseQueryVersionReply(r.Raw); ok {
			x.log.Info().Uint32("major", v.MajorVersion()).Uint32("minor", v.MinorVersion()).Msg("Composite")
		}
	case versionXFixes:
		if v, ok := xfixes.ParseQueryVersionReply(r.Raw); ok {
			x.log.Info().Uint32("major", v.MajorVersion()).Uint32("minor", v.MinorVersion()).Msg("XFixes")
		}
	case versionRes:
		if v, ok := res.ParseQueryVersionReply(r.Raw); ok {
			x.log.Info().Uint16("major", v.ServerMajor()).Uint16("minor", v.ServerMinor()).Msg("X-Resource")
		}
	}
}

// noteTime records the server time of the latest timestamped event; 0 is
// CurrentTime and carries none.
func (x *XWM) noteTime(t uint32) {
	if t != 0 {
		x.lastTime = t
	}
}
