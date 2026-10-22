package xwm

import (
	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

// Override-redirect windows are tracked, never managed: the XWM reads their
// window type once at map, pairs them with their wl_surface and reports the
// mapped rect to the host in layout coordinates.

// maxTypeWords bounds the _NET_WM_WINDOW_TYPE read.
const maxTypeWords = 32

// requestOverrideType asks for the window type of a freshly mapped override
// window; the reply completes its readiness.
func (x *XWM) requestOverrideType(s Slot) {
	rec := x.tab.rec(s)
	seq := xproto.GetProperty(x.conn.Buf(), false, xproto.Window(rec.xid),
		xproto.Atom(x.atoms[atomNetWMWindowType]), xproto.AtomAny, 0, maxTypeWords)
	x.expect(seq, kindOverrideType, x.tab.tag(s), 0)
	x.wake()
}

func (x *XWM) onOverrideType(s Slot, rec *record, r *xturbo.Reply) {
	rec.noInput = false
	if rep, ok := xproto.ParseGetPropertyReply(r.Raw); ok && rep.Format() == 32 {
		val := rep.Value()
		for i := 0; i+4 <= len(val); i += 4 {
			switch x.windowType(word(val, i/4)) {
			case atomNetWMWindowTypeTooltip, atomNetWMWindowTypeNotification, atomNetWMWindowTypeDND:
				rec.noInput = true
			}
		}
	}
	x.overrideReady(s)
}

// overrideReady marks a mapped override window typed and pairs it.
func (x *XWM) overrideReady(s Slot) {
	rec := x.tab.rec(s)
	if rec == nil || rec.kind != kindUnmanaged || !rec.mapped {
		return
	}
	rec.ready = true
	x.tryPair(s)
	x.overrideMapped(s, rec)
}

// overrideMapped reports the window once it is mapped, typed and paired.
func (x *XWM) overrideMapped(s Slot, rec *record) {
	if x.host == nil || rec.ovPaired || !rec.mapped || !rec.ready || rec.surface == 0 {
		return
	}
	rec.ovPaired = true
	rec.ov = x.layoutRect(rec)
	o := rec.ov
	x.host.OverrideMapped(s, rec.xid, rec.surface, int(o.x), int(o.y), int(o.w), int(o.h), rec.noInput)
}

// overrideUnmapped ends the mapped life of an override window; a remap reads
// the type again and pairs with a new surface.
func (x *XWM) overrideUnmapped(s Slot, rec *record) {
	rec.ready, rec.serial, rec.surface, rec.noInput = false, 0, 0, false
	rec.gen++ // a type reply still in flight names a dead tag
	if rec.gen == 0 {
		rec.gen = 1
	}
	if rec.ovPaired {
		rec.ovPaired = false
		x.host.OverrideGone(s, false)
	}
}

func (x *XWM) layoutRect(rec *record) ovRect {
	lx, ly := x.space.ToLayout(int(rec.x), int(rec.y))
	return ovRect{int32(lx), int32(ly), int32(rec.w), int32(rec.h)}
}

// queueOverride schedules a geometry report for s; flushOverrides sends it
// once per iteration, only if the layout rect changed.
func (x *XWM) queueOverride(s Slot, rec *record) {
	if rec.ovQueued || s == 0 {
		return
	}
	if len(x.ovDirty) == cap(x.ovDirty) {
		x.flushOverrides()
	}
	rec.ovQueued = true
	x.ovDirty = append(x.ovDirty, s)
}

func (x *XWM) flushOverrides() {
	for _, s := range x.ovDirty {
		rec := x.tab.rec(s)
		if rec == nil || rec.kind != kindUnmanaged || !rec.ovQueued {
			continue
		}
		rec.ovQueued = false
		if !rec.ovPaired {
			continue
		}
		if o := x.layoutRect(rec); o != rec.ov {
			rec.ov = o
			x.host.OverrideMoved(s, int(o.x), int(o.y), int(o.w), int(o.h))
		}
	}
	x.ovDirty = x.ovDirty[:0]
}
