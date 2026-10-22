package xwm

import (
	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

// Property indices (second Expect argument of kindProp) and their bits in
// record.pending. propGeom is the GetGeometry bit.
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
	propCount
)

const (
	propGeom    uint16 = 1 << propCount
	propMaskAll uint16 = propGeom<<1 - 1
)

// onPropertyNotify handles a changed property of a managed window.
func (x *XWM) onPropertyNotify(ev *xturbo.Event) { _ = ev }

// onPropReply decodes one GetProperty reply into the record.
func (x *XWM) onPropReply(s Slot, rec *record, r *xturbo.Reply) { _, _, _ = s, rec, r }

var _ = xproto.AtomWmClass
