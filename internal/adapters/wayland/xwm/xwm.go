//lint:file-ignore U1000 pairing, focus and stacking state is used by the flows that build on this skeleton

// Package xwm is the built-in X11 window manager for Xwayland: it speaks the
// X protocol over the -wm socket with xturbo and reports managed windows to
// the Wayland server through the unexported host interface.
//
// Threading: Boot runs on the starter goroutine and blocks. The XWM it returns
// is then handed to the display goroutine, which owns it exclusively; nothing
// in this package is locked.
package xwm

import (
	"encoding/binary"
	"errors"
	"fmt"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/composite"
	"bnema.dev/xturbo/res"
	"bnema.dev/xturbo/xfixes"
	"bnema.dev/xturbo/xproto"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/zerowrap"
)

var (
	// ErrWMExists is returned by Boot when another client already redirects
	// the root window's children.
	ErrWMExists = errors.New("xwm: another window manager owns the root window")
	// ErrNoComposite is returned by Boot when the X server lacks Composite.
	ErrNoComposite = errors.New("xwm: X server has no Composite extension")
)

// Stats are connection and event counters, for diagnostics.
type Stats struct {
	Events, Replies, Errors, Unattributed, Stale, RingFull, LegacyID, UnmanagedSeen uint64
}

// Version request ids, carried in the first Expect argument of kindVersion.
const (
	versionComposite uint32 = iota + 1
	versionXFixes
	versionRes
)

const unpairedCap = 64

// XWM manages the windows of one Xwayland. Display goroutine only, after Boot.
type XWM struct {
	conn  *xturbo.Conn
	log   zerowrap.Logger
	root  uint32
	wmWin uint32
	atoms [atomCount]uint32
	tab   *table
	h     xturbo.Handler
	stats Stats

	// attachment
	host    host
	src     fdSource
	post    func(func())
	flushFn func()

	posted, writable, closed bool
	bootErr                  error
	warnedRing, warnedFull   bool

	scratch []byte // encode buffer for 32-bit property values, 4 bytes per slot

	// Shared by the map, configure and focus flows.
	dirty      []Slot   // slots with a pending configure, capacity == table size
	clients    []uint32 // _NET_CLIENT_LIST, map order
	stacking   []uint32 // _NET_CLIENT_LIST_STACKING, bottom to top
	active     Slot     // slot named by _NET_ACTIVE_WINDOW, 0 = none
	refocus    bool     // a FocusIn was denied; focus is re-issued in flush
	listsDirty uint8    // client lists to rewrite in flush
	unpaired   [unpairedCap]struct{ serial, surface uint64 }
	unpairedN  int
}

func newXWM(conn *xturbo.Conn, log zerowrap.Logger) *XWM {
	x := &XWM{conn: conn, log: log, root: conn.Root(), tab: newTable(initialSlots)}
	x.h = xturbo.Handler{OnEvent: x.onEvent, OnReply: x.onReply, OnError: x.onError}
	x.fit()
	return x
}

// fit sizes the per-slot buffers to the table; called after every insert.
func (x *XWM) fit() {
	n := len(x.tab.recs)
	if cap(x.dirty) < n {
		x.dirty = append(make([]Slot, 0, n), x.dirty...)
		x.clients = append(make([]uint32, 0, n), x.clients...)
		x.stacking = append(make([]uint32, 0, n), x.stacking...)
		x.scratch = make([]byte, 0, 4*n)
	}
}

// insert adds a record for xid; 0 means the table is full.
func (x *XWM) insert(xid uint32) Slot {
	s := x.tab.insert(xid)
	if s == 0 {
		if !x.warnedFull {
			x.warnedFull = true
			x.log.Warn().Int("windows", x.tab.n).Msg("X11 window table full, window ignored")
		}
		return 0
	}
	x.fit()
	return s
}

// expect registers seq, tolerating a full ring: the reply then arrives
// unattributed and is dropped.
func (x *XWM) expect(seq xturbo.Seq, kind uint16, a0, a1 uint32) {
	if seq == 0 {
		return
	}
	if err := x.conn.Expect(seq, kind, a0, a1, 0, 0); err != nil {
		x.stats.RingFull++
		if !x.warnedRing {
			x.warnedRing = true
			x.log.Warn().Err(err).Msg("X11 request not tracked")
		}
	}
}

// setProp32 queues ChangeProperty (replace, format 32) for vals.
func (x *XWM) setProp32(win uint32, prop int, typ xproto.Atom, vals []uint32) {
	n := 4 * len(vals)
	if cap(x.scratch) < n {
		x.scratch = make([]byte, 0, n)
	}
	p := x.scratch[:n]
	for i, v := range vals {
		binary.LittleEndian.PutUint32(p[4*i:], v)
	}
	xproto.ChangeProperty(x.conn.Buf(), xproto.PropModeReplace, xproto.Window(win),
		xproto.Atom(x.atoms[prop]), typ, 32, uint32(len(vals)), p)
}

// Boot connects to the X server on wmFD and becomes its window manager. It
// blocks (about 25 s at worst) and must not run on the display goroutine: the
// handshake needs Xwayland, which needs the display goroutine to serve its
// Wayland requests. It takes ownership of wmFD and closes it on failure.
func Boot(wmFD int, log zerowrap.Logger) (*XWM, error) {
	conn, err := xturbo.NewConn(wmFD)
	if err != nil {
		return nil, fmt.Errorf("xwm: connect: %w", err)
	}
	x := newXWM(conn, log)
	if err := x.boot(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return x, nil
}

func (x *XWM) boot() error {
	conn := x.conn
	ext := conn.Ext()
	if !ext.Info(xturbo.ExtComposite).Present {
		return ErrNoComposite
	}
	var visual, depth uint32
	if it := xproto.NewSetupIter(conn.SetupBytes(), 1); it.Next() {
		if roots := it.Value().Roots(); roots.Next() {
			visual, depth = uint32(roots.Value().RootVisual()), uint32(roots.Value().RootDepth())
		}
	}
	x.log.Info().Uint32("root", x.root).Uint32("visual", visual).Uint32("depth", depth).
		Bool("xfixes", ext.Info(xturbo.ExtXFixes).Present).
		Bool("res", ext.Info(xturbo.ExtRes).Present).Msg("X server")

	if err := conn.InternAtoms(atomNames[:], false, x.atoms[:]); err != nil {
		return fmt.Errorf("xwm: intern atoms: %w", err)
	}
	xid, err := conn.NewXID()
	if err != nil {
		return fmt.Errorf("xwm: allocate window id: %w", err)
	}
	x.wmWin = xid

	b := conn.Buf()
	x.expect(composite.QueryVersion(b, 0, 4), kindVersion, versionComposite, 0)
	if ext.Info(xturbo.ExtXFixes).Present {
		x.expect(xfixes.QueryVersion(b, 6, 0), kindVersion, versionXFixes, 0)
	}
	if ext.Info(xturbo.ExtRes).Present {
		x.expect(res.QueryVersion(b, 1, 2), kindVersion, versionRes, 0)
	}

	root := xproto.Window(x.root)
	var rootMask xproto.ChangeWindowAttributesValues
	rootMask.SetEventMask(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify | xproto.EventMaskPropertyChange)
	x.expect(xproto.ChangeWindowAttributes(b, root, &rootMask), kindRootMask, 0, 0)
	composite.RedirectSubwindows(b, root, composite.RedirectManual)

	var wv xproto.CreateWindowValues
	wv.SetEventMask(0)
	xproto.CreateWindow(b, 0, xproto.Window(x.wmWin), root, -1, -1, 1, 1, 0, xproto.WindowClassInputOutput, 0, &wv)
	name := []byte("neferwl")
	xproto.ChangeProperty(b, xproto.PropModeReplace, xproto.Window(x.wmWin), xproto.Atom(x.atoms[atomNetWMName]),
		xproto.Atom(x.atoms[atomUTF8String]), 8, uint32(len(name)), name)
	x.setProp32(x.root, atomNetSupportingWMCheck, xproto.AtomWindow, []uint32{x.wmWin})
	x.setProp32(x.wmWin, atomNetSupportingWMCheck, xproto.AtomWindow, []uint32{x.wmWin})
	xproto.SetSelectionOwner(b, xproto.Window(x.wmWin), xproto.Atom(x.atoms[atomWMS0]), 0)
	xproto.SetSelectionOwner(b, xproto.Window(x.wmWin), xproto.Atom(x.atoms[atomNetWMCMS0]), 0)

	supported := [...]int{
		atomNetWMState, atomNetActiveWindow, atomNetCloseWindow, atomNetWMStateFocused,
		atomNetWMStateModal, atomNetWMStateFullscreen, atomNetWMStateHidden, atomNetClientList,
		atomNetClientListStacking, atomNetSupportingWMCheck, atomNetWMName, atomNetWMPID,
		atomNetWMWindowType,
	}
	var list [len(supported)]uint32
	for i, a := range supported {
		list[i] = x.atoms[a]
	}
	x.setProp32(x.root, atomNetSupported, xproto.AtomAtom, list[:])
	x.setProp32(x.root, atomNetActiveWindow, xproto.AtomWindow, []uint32{0})
	x.setProp32(x.root, atomNetClientList, xproto.AtomWindow, nil)
	x.setProp32(x.root, atomNetClientListStacking, xproto.AtomWindow, nil)
	xproto.SetInputFocus(b, xproto.InputFocusNone, xproto.WindowNone, 0)
	if err := b.Err(); err != nil {
		return fmt.Errorf("xwm: encode setup requests: %w", err)
	}
	if err := conn.Sync(&x.h); err != nil {
		return fmt.Errorf("xwm: root setup: %w", err)
	}
	if x.bootErr != nil {
		return x.bootErr
	}

	// Windows that exist already (Xwayland restarted under a live client set).
	x.expect(xproto.QueryTree(b, root), kindTree, 0, 0)
	if err := conn.Sync(&x.h); err != nil {
		return fmt.Errorf("xwm: query tree: %w", err)
	}
	return x.bootErr
}

// Attach registers the X socket with the display and hands the windows found
// at boot to h. Display goroutine only.
func (x *XWM) Attach(d *server.Display, h host) error {
	src, err := d.AddFDRW(x.conn.Fd(), x.onReadable, x.onWritable)
	if err != nil {
		return fmt.Errorf("xwm: watch X socket: %w", err)
	}
	x.attach(src, h, d.Post)
	return nil
}

// attach is Attach over its seams: the event source and the display's Post.
func (x *XWM) attach(src fdSource, h host, post func(func())) {
	x.src, x.host, x.post = src, h, post
	x.flushFn = func() {
		x.posted = false
		x.flush()
	}
	for i := range x.tab.recs {
		r := &x.tab.recs[i]
		if r.kind != kindManaged || r.id != 0 {
			continue
		}
		s := Slot(i + 1)
		x.adopt(s)
		if r.mapping { // MapRequest or viewable before Attach
			r.mapping = false
			x.beginMap(s)
		}
	}
	x.flush()
}

// Close detaches from the display, drops every window from the host and closes
// the X connection. Idempotent.
func (x *XWM) Close() {
	if x.closed {
		return
	}
	x.closed = true
	if x.src != nil {
		x.src.Remove()
		x.src = nil
	}
	if x.host != nil {
		for i := range x.tab.recs {
			if r := &x.tab.recs[i]; r.kind == kindManaged && r.id != 0 {
				r.id = 0
				x.host.Dissociate(Slot(i+1), true)
			}
		}
	}
	x.unpairedN = 0
	_ = x.conn.Close()
}

// CloseConn closes the connection of an XWM that was never attached.
func (x *XWM) CloseConn() {
	if !x.closed {
		x.closed = true
		_ = x.conn.Close()
	}
}

// fail ends window management after an unrecoverable connection error. Xwayland
// keeps running; its windows no longer map.
func (x *XWM) fail(err error) {
	if x.closed {
		return
	}
	x.log.Warn().Err(err).Msg("X11 window management stopped")
	x.Close()
}

// Stats returns the counters.
func (x *XWM) Stats() Stats { return x.stats }

// Slot returns the slot of the X window xid, or 0.
func (x *XWM) Slot(xid uint32) Slot { return x.tab.lookup(xid) }

func (x *XWM) onReadable() {
	if x.closed {
		return
	}
	if err := x.conn.Dispatch(&x.h); err != nil {
		x.fail(err)
		return
	}
	x.flush()
}

func (x *XWM) onWritable() { x.flush() }

// wake schedules one flush at the end of the current display iteration.
func (x *XWM) wake() {
	if x.posted || x.closed || x.post == nil {
		return
	}
	x.posted = true
	x.post(x.flushFn)
}

// flush sends what the handlers queued: pending configures, an XID prefetch,
// then the socket, arming POLLOUT only while it is full.
func (x *XWM) flush() {
	if x.closed {
		return
	}
	x.flushConfigures()
	if x.conn.XIDsLow() {
		_ = x.conn.PrefetchXIDs()
	}
	err := x.conn.Flush()
	switch {
	case err == nil:
		if x.writable {
			x.setWritable(false)
		}
	case errors.Is(err, xturbo.ErrWouldBlock):
		if !x.writable {
			x.setWritable(true)
		}
	default:
		x.fail(err)
	}
}

func (x *XWM) setWritable(on bool) {
	x.writable = on
	if x.src == nil {
		return
	}
	if err := x.src.SetWritable(on); err != nil {
		x.fail(err)
	}
}
