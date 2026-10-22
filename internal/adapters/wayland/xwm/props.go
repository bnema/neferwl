package xwm

import (
	"bytes"
	"encoding/binary"
	"strings"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/res"
	"bnema.dev/xturbo/xproto"
)

// Property indices (second Expect argument of kindProp) and their bits in
// record.pending. propGeom is the GetGeometry bit; propClientIDs is raised
// while a QueryClientIds reply stands in for a missing _NET_WM_PID.
const (
	propClass = iota
	propName
	propNetName
	propHints
	propNormalHints
	propProtocols
	propTransientFor
	propWindowType
	propState
	propPID
	propBypass
	propCount
)

const (
	propGeom      uint16 = 1 << propCount
	propClientIDs uint16 = propGeom << 1
	// propMaskAll is what a map waits for: every property and the geometry.
	propMaskAll uint16 = propGeom<<1 - 1
)

// WM_NORMAL_HINTS flags and WM_HINTS flags.
const (
	hintInput   = 1 << 0
	hintMinSize = 1 << 4
	hintMaxSize = 1 << 5
)

// maxPropWords bounds one GetProperty (4 KiB), so titles are truncated there.
const maxPropWords = 1024

// propIndex maps a property atom to its index, or -1 when it is not watched.
func (x *XWM) propIndex(a uint32) int {
	switch a {
	case uint32(xproto.AtomWmClass):
		return propClass
	case uint32(xproto.AtomWmName):
		return propName
	case uint32(xproto.AtomWmHints):
		return propHints
	case uint32(xproto.AtomWmNormalHints):
		return propNormalHints
	case uint32(xproto.AtomWmTransientFor):
		return propTransientFor
	case x.atoms[atomNetWMName]:
		return propNetName
	case x.atoms[atomWMProtocols]:
		return propProtocols
	case x.atoms[atomNetWMWindowType]:
		return propWindowType
	case x.atoms[atomNetWMState]:
		return propState
	case x.atoms[atomNetWMPID]:
		return propPID
	case x.atoms[atomNetWMBypassCompositor]:
		return propBypass
	}
	return -1
}

func (x *XWM) propAtom(idx int) xproto.Atom {
	switch idx {
	case propClass:
		return xproto.AtomWmClass
	case propName:
		return xproto.AtomWmName
	case propHints:
		return xproto.AtomWmHints
	case propNormalHints:
		return xproto.AtomWmNormalHints
	case propTransientFor:
		return xproto.AtomWmTransientFor
	case propNetName:
		return xproto.Atom(x.atoms[atomNetWMName])
	case propProtocols:
		return xproto.Atom(x.atoms[atomWMProtocols])
	case propWindowType:
		return xproto.Atom(x.atoms[atomNetWMWindowType])
	case propState:
		return xproto.Atom(x.atoms[atomNetWMState])
	case propBypass:
		return xproto.Atom(x.atoms[atomNetWMBypassCompositor])
	default:
		return xproto.Atom(x.atoms[atomNetWMPID])
	}
}

// requestProp queues a GetProperty for property idx of s.
func (x *XWM) requestProp(s Slot, idx int) {
	rec := x.tab.rec(s)
	seq := xproto.GetProperty(x.conn.Buf(), false, xproto.Window(rec.xid), x.propAtom(idx), xproto.AtomAny, 0, maxPropWords)
	rec.pending |= 1 << idx
	x.expect(seq, kindProp, x.tab.tag(s), uint32(idx))
}

// onPropertyNotify handles a changed property of a managed window that is
// being mapped or is mapped; unwatched atoms cost nothing.
func (x *XWM) onPropertyNotify(ev *xturbo.Event) {
	e := xproto.PropertyNotifyOf(ev)
	if uint32(e.Window()) == x.selWin && x.selWin != 0 {
		x.onSelProperty(ev)
		return
	}
	if x.onRequestorProperty(e) {
		return
	}
	s := x.tab.lookup(uint32(e.Window()))
	if s == 0 {
		return
	}
	if rec := x.tab.rec(s); rec.kind != kindManaged || !rec.mapping {
		return
	} else if rec.ready && x.propIndex(uint32(e.Atom())) == propState {
		// The XWM owns _NET_WM_STATE of a mapped window: our own writes
		// would feed back, and clients change it with ClientMessages.
		return
	}
	idx := x.propIndex(uint32(e.Atom()))
	if idx < 0 {
		return
	}
	if e.State() == xproto.PropertyDelete {
		x.applyProp(s, idx, 0, nil)
		return
	}
	x.requestProp(s, idx)
}

// onPropReply decodes one GetProperty reply into the record. An absent
// property (format 0) clears its fields.
func (x *XWM) onPropReply(s Slot, _ *record, r *xturbo.Reply) {
	idx := int(r.Args[1])
	if idx < 0 || idx >= propCount {
		return
	}
	var format uint8
	var val []byte
	if rep, ok := xproto.ParseGetPropertyReply(r.Raw); ok {
		format, val = rep.Format(), rep.Value()
	}
	x.applyProp(s, idx, format, val)
	x.tab.rec(s).pending &^= 1 << idx
	x.checkReady(s)
}

func word(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[4*i:]) }

// applyProp stores one decoded property. It does not allocate unless a string
// changed. Host calls come last: the record is not touched after them.
func (x *XWM) applyProp(s Slot, idx int, format uint8, val []byte) {
	rec := x.tab.rec(s)
	if idx == propClass || idx == propName || idx == propNetName {
		if format != 8 {
			val = nil
		}
	} else if format != 32 {
		val = nil
	}
	switch idx {
	case propClass:
		inst, class := val, []byte(nil)
		if i := bytes.IndexByte(val, 0); i >= 0 {
			inst, class = val[:i], val[i+1:]
			if j := bytes.IndexByte(class, 0); j >= 0 {
				class = class[:j]
			}
		}
		app := class
		if len(app) == 0 {
			app = inst
		}
		if rec.class == string(app) {
			return
		}
		cls := strings.ToValidUTF8(string(app), "\uFFFD")
		if rec.class == cls {
			return
		}
		rec.class = cls
		if rec.ready {
			x.host.AppID(s, rec.class)
		}
	case propName:
		if !rec.hasNetName {
			x.setTitle(s, val)
		}
	case propNetName:
		had := rec.hasNetName
		rec.hasNetName = len(val) > 0
		if rec.hasNetName {
			x.setTitle(s, val)
		} else if had && rec.mapping {
			x.requestProp(s, propName)
		}
	case propHints:
		rec.input = true
		if len(val) >= 8 && word(val, 0)&hintInput != 0 {
			rec.input = word(val, 1) != 0
		}
	case propNormalHints:
		rec.minW, rec.minH, rec.maxW, rec.maxH = 0, 0, 0, 0
		if len(val) >= 36 {
			fl := word(val, 0)
			if fl&hintMinSize != 0 {
				rec.minW, rec.minH = int32(word(val, 5)), int32(word(val, 6))
			}
			if fl&hintMaxSize != 0 {
				rec.maxW, rec.maxH = int32(word(val, 7)), int32(word(val, 8))
			}
		}
	case propProtocols:
		rec.deleteWindow, rec.takeFocus = false, false
		for i := 0; i+4 <= len(val); i += 4 {
			switch binary.LittleEndian.Uint32(val[i:]) {
			case x.atoms[atomWMDeleteWindow]:
				rec.deleteWindow = true
			case x.atoms[atomWMTakeFocus]:
				rec.takeFocus = true
			}
		}
	case propTransientFor:
		var t uint32
		if len(val) >= 4 {
			t = word(val, 0)
		}
		if t == rec.transient {
			return
		}
		rec.transient = t
		if rec.ready {
			x.host.Parent(s, x.managedSlot(t))
		}
	case propWindowType:
		rec.wtype = 0
		for i := 0; i+4 <= len(val); i += 4 {
			if t := x.windowType(binary.LittleEndian.Uint32(val[i:])); t != 0 {
				rec.wtype = t
				break
			}
		}
	case propState:
		var modal, fs bool
		for i := 0; i+4 <= len(val); i += 4 {
			switch binary.LittleEndian.Uint32(val[i:]) {
			case x.atoms[atomNetWMStateModal]:
				modal = true
			case x.atoms[atomNetWMStateFullscreen]:
				fs = true
			}
		}
		changed := fs != rec.fullscreen
		rec.modal, rec.fullscreen = modal, fs
		if changed && rec.ready {
			x.host.Fullscreen(s, fs)
		}
	case propBypass:
		// Kept for a later VRR/tearing policy; direct scanout and tearing
		// do not depend on it. 2 ("do not bypass") is ignored on purpose.
		rec.bypass = 0
		if len(val) >= 4 {
			rec.bypass = uint8(min(word(val, 0), 2))
		}
		x.log.Debug().Uint32("xid", rec.xid).Uint8("bypass", rec.bypass).Msg("_NET_WM_BYPASS_COMPOSITOR")
	case propPID:
		if len(val) >= 4 {
			rec.pid = int32(word(val, 0))
			return
		}
		rec.pid = 0
		if !rec.ready && rec.pending&propClientIDs == 0 && x.conn.Ext().Info(xturbo.ExtRes).Present {
			x.queryClientIDs(s)
		}
	}
}

// setTitle stores a title when it differs; only a change allocates.
func (x *XWM) setTitle(s Slot, val []byte) {
	rec := x.tab.rec(s)
	if rec.title == string(val) {
		return
	}
	t := strings.ToValidUTF8(string(val), "\uFFFD")
	if rec.title == t {
		return
	}
	rec.title = t
	if rec.ready {
		x.host.Title(s, rec.title)
	}
}

// windowType returns the atom enum of a recognised _NET_WM_WINDOW_TYPE atom,
// or 0.
func (x *XWM) windowType(a uint32) uint8 {
	for t := atomNetWMWindowTypeNormal; t <= atomNetWMWindowTypeDND; t++ {
		if x.atoms[t] == a {
			return uint8(t)
		}
	}
	return 0
}

// floatingType reports whether a window type floats by itself.
func floatingType(t uint8) bool {
	switch int(t) {
	case atomNetWMWindowTypeDialog, atomNetWMWindowTypeUtility, atomNetWMWindowTypeSplash, atomNetWMWindowTypeToolbar:
		return true
	}
	return false
}

// managedSlot returns the slot of the managed window xid, or 0.
func (x *XWM) managedSlot(xid uint32) Slot {
	if xid == 0 {
		return 0
	}
	if s := x.tab.lookup(xid); s != 0 && x.tab.rec(s).kind == kindManaged {
		return s
	}
	return 0
}

// queryClientIDs asks XRes for the PID of the client owning s, which stands
// in for a missing _NET_WM_PID: Ready waits for it (propClientIDs).
func (x *XWM) queryClientIDs(s Slot) {
	rec := x.tab.rec(s)
	specs := [1]res.ClientIdSpec{{Client: rec.xid, Mask: res.ClientIdMaskLocalClientPID}}
	seq := res.QueryClientIds(x.conn.Buf(), specs[:])
	if seq == 0 {
		return
	}
	rec.pending |= propClientIDs
	x.expect(seq, kindClientIds, x.tab.tag(s), 0)
}

// onClientIdsReply stores the PID XRes found and releases the map.
func (x *XWM) onClientIdsReply(s Slot, rec *record, r *xturbo.Reply) {
	if q, ok := res.ParseQueryClientIdsReply(r.Raw); ok {
		it := q.Ids()
		for it.Next() {
			v := it.Value()
			if v.Spec().Mask == res.ClientIdMaskLocalClientPID && v.Value().Len() > 0 {
				rec.pid = int32(v.Value().At(0))
				break
			}
		}
	}
	rec.pending &^= propClientIDs
	x.checkReady(s)
}
