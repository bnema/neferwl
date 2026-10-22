package xwm

import (
	"encoding/binary"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xfixes"
	"bnema.dev/xturbo/xproto"
)

// The selection bridge: text-only CLIPBOARD and PRIMARY between X clients and
// the Wayland clipboard. Display goroutine only; nothing here is locked.

// Selection kinds, in the order clipboard.go uses.
const (
	SelClipboard = iota
	SelPrimary
	selKinds
)

// textMimes are the MIME types the bridge offers, in the order of preference;
// mimeTarget gives the atom an X owner must advertise for each.
var (
	textMimes  = [5]string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING", "TEXT"}
	mimeTarget = [5]int{atomUTF8String, atomString, atomUTF8String, atomString, atomUTF8String}
)

// selState is one selection.
type selState struct {
	atom  uint32
	gen   uint32 // bumped on every ownership change; stales TARGETS probes
	owner uint32 // X owner window other than selWin, 0 none
}

// selBridge is the XWM's selection state.
type selBridge struct {
	st    [selKinds]selState
	mimes [len(textMimes)]string // scratch handed to SelectionOwned
}

// bootSelection creates the selection window and asks for ownership events.
// Without XFixes the bridge stays off.
func (x *XWM) bootSelection(root xproto.Window) {
	if !x.conn.Ext().Info(xturbo.ExtXFixes).Present {
		x.log.Warn().Msg("X server has no XFixes: clipboard bridge disabled")
		return
	}
	xid := x.selWin
	b := x.conn.Buf()
	var wv xproto.CreateWindowValues
	wv.SetEventMask(xproto.EventMaskPropertyChange)
	xproto.CreateWindow(b, 0, xproto.Window(xid), root, -1, -1, 1, 1, 0, xproto.WindowClassInputOnly, 0, &wv)
	x.sel.st[SelClipboard].atom = x.atoms[atomClipboard]
	x.sel.st[SelPrimary].atom = x.atoms[atomPrimary]
	const mask = xfixes.SelectionEventMaskSetSelectionOwner | xfixes.SelectionEventMaskSelectionWindowDestroy | xfixes.SelectionEventMaskSelectionClientClose
	for k := range x.sel.st {
		xfixes.SelectSelectionInput(b, root, xproto.Atom(x.sel.st[k].atom), mask)
	}
}

// selKind maps a selection atom to its kind, -1 when it is neither.
func (x *XWM) selKind(atom uint32) int {
	for k := range x.sel.st {
		if x.sel.st[k].atom == atom && atom != 0 {
			return k
		}
	}
	return -1
}

// onSelectionEvent handles the selection events; others are ignored.
func (x *XWM) onSelectionEvent(ev *xturbo.Event) {
	if x.selWin == 0 {
		return
	}
	switch ev.Kind {
	case xfixes.KindSelectionNotify:
		x.onXFixesSelection(ev)
	case xproto.KindSelectionNotify:
		x.onSelectionNotify(ev)
	}
}

// onXFixesSelection: the owner of a selection changed.
func (x *XWM) onXFixesSelection(ev *xturbo.Event) {
	e := xfixes.SelectionNotifyOf(ev)
	k := x.selKind(uint32(e.Selection()))
	if k < 0 {
		return
	}
	st := &x.sel.st[k]
	owner := uint32(e.Owner())
	if owner == x.selWin { // our own SetSelectionOwner echoed back
		return
	}
	st.gen++
	st.owner = owner
	if owner == 0 {
		x.log.Debug().Int("kind", k).Msg("selection: no X owner")
		x.host.SelectionOwned(k, nil)
		return
	}
	x.log.Debug().Int("kind", k).Uint32("owner", owner).Msg("selection: X owner")
	xproto.ConvertSelection(x.conn.Buf(), xproto.Window(x.selWin), xproto.Atom(st.atom),
		xproto.Atom(x.atoms[atomTargets]), xproto.Atom(x.atoms[atomNeferwlTargets]), 0)
	x.wake()
}

// onSelectionNotify: a ConvertSelection we issued was answered.
func (x *XWM) onSelectionNotify(ev *xturbo.Event) {
	e := xproto.SelectionNotifyOf(ev)
	if uint32(e.Requestor()) != x.selWin {
		return
	}
	k := x.selKind(uint32(e.Selection()))
	if k < 0 {
		return
	}
	if uint32(e.Target()) == x.atoms[atomTargets] {
		if e.Property() == 0 {
			if x.sel.st[k].owner != 0 {
				x.host.SelectionOwned(k, nil)
			}
			return
		}
		seq := xproto.GetProperty(x.conn.Buf(), true, xproto.Window(x.selWin), xproto.Atom(x.atoms[atomNeferwlTargets]), 0, 0, 256)
		x.expect(seq, kindSelTargets, uint32(k), x.sel.st[k].gen)
		x.wake()
		return
	}
}

// onTargetsReply turns the owner's TARGETS list into the MIME types offered
// to Wayland: the text subset, empty for images, files and the like.
func (x *XWM) onTargetsReply(r *xturbo.Reply) {
	k := int(r.Args[0])
	if k < 0 || k >= selKinds || r.Args[1] != x.sel.st[k].gen {
		return
	}
	g, ok := xproto.ParseGetPropertyReply(r.Raw)
	if !ok {
		return
	}
	var have [atomCount]bool
	if g.Format() == 32 {
		v := g.Value()
		for i := 0; i+4 <= len(v); i += 4 {
			a := binary.LittleEndian.Uint32(v[i:])
			for _, t := range [...]int{atomUTF8String, atomString, atomText} {
				if a == x.atoms[t] {
					have[t] = true
				}
			}
		}
	}
	n := 0
	for i, m := range textMimes {
		if have[mimeTarget[i]] {
			x.sel.mimes[n] = m
			n++
		}
	}
	if n == 0 {
		x.host.SelectionOwned(k, nil)
		return
	}
	x.host.SelectionOwned(k, x.sel.mimes[:n])
}

// onSelError: a selection request failed; the awaited reply never comes.
func (x *XWM) onSelError(e *xturbo.XError) {}

func (x *XWM) onSelDataReply(r *xturbo.Reply) {}
