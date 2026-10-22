package xwm

import (
	"encoding/binary"
	"errors"
	"time"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xfixes"
	"bnema.dev/xturbo/xproto"
	"golang.org/x/sys/unix"
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
	probe uint32 // gen when the TARGETS ConvertSelection was issued
	owner uint32 // X owner window other than selWin, 0 none
	ours  bool   // selWin owns the selection (Wayland -> X)
	// wl is what the Wayland side offers while ours: raw text availability.
	wlUTF8, wlPlain bool
	wlMime          [len(textMimes)]string // offered text MIMEs, in textMimes order, "" absent
}

const (
	chunk     = 65536 // every transfer step moves at most this many bytes
	nXfer     = 4     // concurrent transfers per direction, and pool buffers
	tickEvery = time.Second
	xIdleMax  = 10 // ticks without X-side progress
	rIdleMax  = 30 // ticks of a reader that does not drain its pipe
)

// bufPool is the fixed set of transfer buffers.
type bufPool struct {
	bufs [nXfer][chunk]byte
	free [nXfer]int8
	n    int
}

func (p *bufPool) init() {
	for i := range p.free {
		p.free[i] = int8(i)
	}
	p.n = nXfer
}

// get returns a free buffer index, -1 when the pool is exhausted.
func (p *bufPool) get() int {
	if p.n == 0 {
		return -1
	}
	p.n--
	return int(p.free[p.n])
}

func (p *bufPool) put(i int) {
	p.free[p.n] = int8(i)
	p.n++
}

// X -> Wayland transfer states.
const (
	inFree   uint8 = iota
	inQueued       // waiting for the conversion property to be free
	inConverting
	inWaitIncr // waiting for the next INCR chunk (PropertyNotify)
	inWriting  // a buffer is pending on the Wayland reader's pipe
	inReading  // a GetProperty is outstanding
)

// xin is one X -> Wayland transfer.
type xin struct {
	x      *XWM
	idx    int
	state  uint8
	kind   int
	fd     int
	target uint32
	order  uint64
	gen    uint32 // stales replies of an aborted transfer
	incr   bool
	buf    int
	off, n int
	words  uint32 // long offset of the next piece of a long property
	more   bool   // the last piece left bytes in the property
	ready  bool   // the owner wrote the next INCR chunk while a buffer was pending
	idle   int
	src    fdSource
	onR    func() // pipe hangup
	onW    func() // pipe writable
}

// selBridge is the XWM's selection state.
type selBridge struct {
	st    [selKinds]selState
	mimes [len(textMimes)]string // scratch handed to SelectionOwned
	pool  bufPool
	in    [nXfer]xin
	out   [nXfer]xout
	order uint64
	// active is the transfer that owns the conversion property, -1 none.
	active  int
	timerOn bool
	cancel  func()
	tickFn  func()
}

func (x *XWM) initSelection() {
	b := &x.sel
	b.pool.init()
	b.active = -1
	b.tickFn = x.selTick
	for i := range b.in {
		t := &b.in[i]
		t.x, t.idx, t.buf = x, i, -1
		t.onR = func() { t.x.inAbort(t.idx, true) }
		t.onW = func() { t.x.inWritable(t.idx) }
	}
	for i := range b.out {
		o := &b.out[i]
		o.x, o.idx, o.buf, o.fd = x, i, -1, -1
		o.onR = func() { o.x.outReadable(o.idx) }
	}
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
	case xproto.KindSelectionRequest:
		x.onSelectionRequest(ev)
	case xproto.KindSelectionClear:
		x.onSelectionClear(ev)
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
	st.probe = st.gen
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
	if uint32(e.Target()) != x.atoms[atomTargets] {
		x.onDataNotify(e)
		return
	}
	{
		st := &x.sel.st[k]
		if st.ours || st.owner == 0 || st.probe != st.gen { // stale probe
			return
		}
		if e.Property() == 0 {
			x.host.SelectionOwned(k, nil)
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
	if k < 0 || k >= selKinds || r.Args[1] != x.sel.st[k].gen || x.sel.st[k].ours || x.sel.st[k].owner == 0 || x.sel.st[k].probe != r.Args[1] {
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

// ---- X -> Wayland ----

// SelectionReceive: a Wayland reader wants the X selection kind as mime. The
// XWM takes fd (a write end it now owns) and closes it when done.
func (x *XWM) SelectionReceive(kind int, mime string, fd int) {
	target := uint32(0)
	for i, m := range textMimes {
		if m == mime {
			target = x.atoms[mimeTarget[i]]
			break
		}
	}
	if x.selWin == 0 || x.closed || kind < 0 || kind >= selKinds || target == 0 || x.sel.st[kind].owner == 0 {
		_ = unix.Close(fd)
		return
	}
	if unix.SetNonblock(fd, true) != nil {
		_ = unix.Close(fd)
		return
	}
	for i := range x.sel.in {
		t := &x.sel.in[i]
		if t.state != inFree {
			continue
		}
		x.sel.order++
		t.state, t.kind, t.fd, t.target, t.order = inQueued, kind, fd, target, x.sel.order
		t.incr, t.buf, t.off, t.n, t.words, t.more, t.ready, t.idle, t.src = false, -1, 0, 0, 0, false, false, 0, nil
		x.selEnsureTimer()
		x.inStartNext()
		return
	}
	x.log.Warn().Msg("selection: too many transfers, paste refused")
	_ = unix.Close(fd)
}

// inStartNext starts the oldest queued transfer when none is converting.
func (x *XWM) inStartNext() {
	b := &x.sel
	if b.active >= 0 {
		return
	}
	best := -1
	for i := range b.in {
		if b.in[i].state == inQueued && (best < 0 || b.in[i].order < b.in[best].order) {
			best = i
		}
	}
	if best < 0 {
		return
	}
	t := &b.in[best]
	t.state, t.idle = inConverting, 0
	b.active = best
	xproto.ConvertSelection(x.conn.Buf(), xproto.Window(x.selWin), xproto.Atom(b.st[t.kind].atom),
		xproto.Atom(t.target), xproto.Atom(x.atoms[atomNeferwlData]), 0)
	x.wake()
}

// inFinish ends transfer i and starts the next one.
func (x *XWM) inFinish(i int) {
	b := &x.sel
	t := &b.in[i]
	if t.src != nil {
		t.src.Remove()
		t.src = nil
	}
	if t.fd >= 0 {
		_ = unix.Close(t.fd)
	}
	if t.buf >= 0 {
		b.pool.put(t.buf)
		t.buf = -1
	}
	t.gen++
	t.state, t.fd = inFree, -1
	if b.active == i {
		b.active = -1
	}
	x.inStartNext()
}

// inAbort drops transfer i, deleting the conversion property when X may still
// be writing it.
func (x *XWM) inAbort(i int, del bool) {
	t := &x.sel.in[i]
	if t.state == inFree {
		return
	}
	if del && x.sel.active == i && !x.closed {
		xproto.DeleteProperty(x.conn.Buf(), xproto.Window(x.selWin), xproto.Atom(x.atoms[atomNeferwlData]))
		x.wake()
	}
	x.inFinish(i)
}

// onDataNotify: the owner answered our data ConvertSelection.
func (x *XWM) onDataNotify(e xproto.SelectionNotifyEvent) {
	i := x.sel.active
	if i < 0 {
		return
	}
	t := &x.sel.in[i]
	if t.state != inConverting || uint32(e.Target()) != t.target {
		return
	}
	if e.Property() == 0 {
		x.inFinish(i)
		return
	}
	t.words = 0
	x.inRequest(i)
}

// inRequest asks for the next piece of the property; delete makes INCR and
// whole-value reads advance.
func (x *XWM) inRequest(i int) {
	t := &x.sel.in[i]
	t.state = inReading
	seq := xproto.GetProperty(x.conn.Buf(), true, xproto.Window(x.selWin), xproto.Atom(x.atoms[atomNeferwlData]),
		0, t.words, chunk/4)
	if seq != 0 {
		if err := x.conn.Expect(seq, kindSelData, uint32(i), t.gen, 0, 0); err != nil {
			x.stats.RingFull++
		}
	}
	x.wake()
}

// onSelDataReply handles one piece of the converted property.
func (x *XWM) onSelDataReply(r *xturbo.Reply) {
	i := int(r.Args[0])
	if i < 0 || i >= nXfer {
		return
	}
	t := &x.sel.in[i]
	if t.state != inReading || t.gen != r.Args[1] {
		return
	}
	g, ok := xproto.ParseGetPropertyReply(r.Raw)
	if !ok || g.Format() == 0 {
		x.inAbort(i, false)
		return
	}
	t.idle = 0
	if uint32(g.Type()) == x.atoms[atomIncr] {
		t.incr, t.state, t.words, t.more = true, inWaitIncr, 0, false
		return
	}
	v := g.Value()
	if g.Format() != 8 || len(v) > chunk {
		x.inAbort(i, true)
		return
	}
	t.more = g.BytesAfter() > 0
	if len(v) == 0 {
		if t.more { // cannot happen: bytes remain only after a non-empty read
			x.inAbort(i, true)
			return
		}
		x.inNext(i)
		return
	}
	b := x.sel.pool.get()
	if b < 0 {
		x.log.Warn().Msg("selection: no free buffer, paste aborted")
		x.inAbort(i, true)
		return
	}
	t.buf = b
	t.n = copy(x.sel.pool.bufs[b][:], v)
	t.off = 0
	t.words += uint32(t.n) / 4
	t.state = inWriting
	x.inDrain(i)
}

// inDrain writes the pending buffer to the reader's pipe.
func (x *XWM) inDrain(i int) {
	t := &x.sel.in[i]
	buf := &x.sel.pool.bufs[t.buf]
	for t.off < t.n {
		n, err := unix.Write(t.fd, buf[t.off:t.n])
		switch {
		case err == nil:
			t.off += n
			t.idle = 0
		case errors.Is(err, unix.EINTR):
		case errors.Is(err, unix.EAGAIN):
			x.inWatch(i, true)
			return
		default: // EPIPE: the reader went away
			x.inAbort(i, true)
			return
		}
	}
	x.sel.pool.put(t.buf)
	t.buf = -1
	if t.src != nil {
		x.inWatch(i, false)
	}
	x.inNext(i)
}

// inWatch arms or disarms writable interest on the reader's pipe.
func (x *XWM) inWatch(i int, on bool) {
	t := &x.sel.in[i]
	if t.src == nil {
		if !on || x.lp == nil {
			if on {
				x.inAbort(i, true)
			}
			return
		}
		src, err := x.lp.AddFD(t.fd, t.onR, t.onW)
		if err != nil {
			x.inAbort(i, true)
			return
		}
		t.src = src
	}
	if err := t.src.SetWritable(on); err != nil {
		x.inAbort(i, true)
	}
}

// inWritable: the pipe has room again.
func (x *XWM) inWritable(i int) {
	t := &x.sel.in[i]
	if t.state == inWriting && t.buf >= 0 {
		x.inDrain(i)
	}
}

// inNext decides what follows a drained (or empty) piece.
func (x *XWM) inNext(i int) {
	t := &x.sel.in[i]
	switch {
	case t.more: // the rest of this property
		x.inRequest(i)
	case t.incr && t.n > 0:
		t.state, t.n, t.words = inWaitIncr, 0, 0
		if t.ready {
			t.ready = false
			x.inRequest(i)
		}
	default: // single value done, or the empty INCR terminator
		x.inFinish(i)
	}
}

// onSelProperty: PropertyNotify on the selection window.
func (x *XWM) onSelProperty(ev *xturbo.Event) {
	e := xproto.PropertyNotifyOf(ev)
	if e.State() != xproto.PropertyNewValue || uint32(e.Atom()) != x.atoms[atomNeferwlData] {
		return
	}
	// (a Deleted state on a requestor is handled by onRequestorProperty)
	i := x.sel.active
	if i < 0 || !x.sel.in[i].incr {
		return
	}
	t := &x.sel.in[i]
	switch t.state {
	case inWaitIncr:
		t.idle = 0
		t.n = 0
		x.inRequest(i)
	case inWriting, inReading:
		t.ready = true // no request while a buffer is pending: backpressure
	}
}

// onSelError: a selection request failed; the awaited reply never comes.
func (x *XWM) onSelError(e *xturbo.XError) {
	if e.Kind == kindSelOut {
		if i := int(e.Args[0]); i >= 0 && i < nXfer && x.sel.out[i].gen == e.Args[1] && x.sel.out[i].state != outFree {
			x.outAbort(i)
		}
		return
	}
	if e.Kind == kindSelData {
		if i := int(e.Args[0]); i >= 0 && i < nXfer && x.sel.in[i].gen == e.Args[1] && x.sel.in[i].state == inReading {
			x.inAbort(i, false)
		}
	}
}

// ---- timeouts ----

func (x *XWM) selEnsureTimer() {
	b := &x.sel
	if b.timerOn || x.lp == nil {
		return
	}
	b.timerOn = true
	b.cancel = x.lp.After(tickEvery, b.tickFn)
}

// selTick ages every transfer by one second and aborts the stalled ones.
func (x *XWM) selTick() {
	b := &x.sel
	b.timerOn, b.cancel = false, nil
	if x.closed {
		return
	}
	live := false
	for i := range b.in {
		t := &b.in[i]
		if t.state == inFree {
			continue
		}
		t.idle++
		limit := xIdleMax
		if t.state == inWriting {
			limit = rIdleMax
		}
		if t.state != inQueued && t.idle >= limit {
			x.log.Warn().Int("kind", t.kind).Msg("selection: transfer timed out")
			x.inAbort(i, true)
			continue
		}
		live = true
	}
	for i := range b.out {
		o := &b.out[i]
		if o.state == outFree {
			continue
		}
		o.idle++
		limit := rIdleMax
		if o.incr && o.n > 0 && !o.credit {
			limit = xIdleMax // the requestor does not delete the property
		}
		if o.idle >= limit {
			x.log.Warn().Int("kind", o.kind).Msg("selection: transfer timed out")
			x.outAbort(i)
			continue
		}
		live = true
	}
	if live {
		x.selEnsureTimer()
	}
}

// selClose aborts every transfer.
func (x *XWM) selClose() {
	for i := range x.sel.out {
		if x.sel.out[i].state != outFree {
			x.outAbort(i)
		}
	}
	for i := range x.sel.in {
		if x.sel.in[i].state != inFree {
			x.inAbort(i, false)
		}
	}
	if x.sel.cancel != nil {
		x.sel.cancel()
		x.sel.cancel = nil
	}
}

// ---- Wayland -> X ----

// Wayland -> X transfer states.
const (
	outFree uint8 = iota
	outActive
)

// xout is one Wayland -> X transfer.
type xout struct {
	x         *XWM
	idx       int
	state     uint8
	kind      int
	requestor uint32
	property  uint32
	target    uint32
	time      uint32
	fd        int
	buf       int
	n         int
	eof       bool
	incr      bool // INCR announced
	credit    bool // the requestor deleted the property: the next chunk may go
	notified  bool // the requestor got its SelectionNotify
	idle      int
	gen       uint32
	src       fdSource
	onR       func()
}

// WaylandSelection: a Wayland client owns kind with these MIME types, or none
// does (mimes nil). The XWM takes X ownership when text is offered.
func (x *XWM) WaylandSelection(kind int, mimes []string) {
	if x.selWin == 0 || x.closed || kind < 0 || kind >= selKinds || x.host == nil {
		return
	}
	st := &x.sel.st[kind]
	st.wlUTF8, st.wlPlain = false, false
	st.wlMime = [len(textMimes)]string{}
	for _, m := range mimes {
		for i, tm := range textMimes {
			if m != tm {
				continue
			}
			st.wlMime[i] = m
			if mimeTarget[i] == atomUTF8String {
				st.wlUTF8 = true
			} else {
				st.wlPlain = true
			}
		}
	}
	for i := range x.sel.out { // the data being served is stale
		if x.sel.out[i].state != outFree && x.sel.out[i].kind == kind {
			x.outAbort(i)
		}
	}
	b := x.conn.Buf()
	if !st.wlUTF8 && !st.wlPlain {
		if st.ours {
			st.ours = false
			xproto.SetSelectionOwner(b, 0, xproto.Atom(st.atom), 0)
			x.wake()
		}
		return
	}
	st.ours = true
	st.gen++
	st.owner = 0
	xproto.SetSelectionOwner(b, xproto.Window(x.selWin), xproto.Atom(st.atom), 0)
	x.log.Debug().Int("kind", kind).Msg("selection: Wayland owner")
	x.wake()
}

// onSelectionClear: someone else took a selection from selWin.
func (x *XWM) onSelectionClear(ev *xturbo.Event) {
	e := xproto.SelectionClearOf(ev)
	k := x.selKind(uint32(e.Selection()))
	if k < 0 || uint32(e.Owner()) != x.selWin {
		return
	}
	x.sel.st[k].ours = false
	for i := range x.sel.out {
		if x.sel.out[i].state != outFree && x.sel.out[i].kind == k {
			x.outAbort(i)
		}
	}
}

// notify sends a SelectionNotify to the requestor; property 0 refuses.
func (x *XWM) notify(requestor, selection, target, property, time uint32) {
	d := xproto.SelectionNotifyEventData{
		Time: xproto.Timestamp(time), Requestor: xproto.Window(requestor),
		Selection: xproto.Atom(selection), Target: xproto.Atom(target), Property: xproto.Atom(property),
	}
	var ev [32]byte
	d.Put(&ev)
	xproto.SendEvent(x.conn.Buf(), false, xproto.Window(requestor), 0, &ev)
	x.wake()
}

// onSelectionRequest: an X client asks selWin for the Wayland selection.
func (x *XWM) onSelectionRequest(ev *xturbo.Event) {
	e := xproto.SelectionRequestOf(ev)
	if uint32(e.Owner()) != x.selWin {
		return
	}
	sel, req, target, prop, time := uint32(e.Selection()), uint32(e.Requestor()), uint32(e.Target()), uint32(e.Property()), uint32(e.Time())
	k := x.selKind(sel)
	if k < 0 || !x.sel.st[k].ours {
		x.notify(req, sel, target, 0, time)
		return
	}
	if prop == 0 { // obsolete client
		prop = target
	}
	st := &x.sel.st[k]
	switch target {
	case x.atoms[atomTargets]:
		var list [5]uint32
		n := 0
		list[n], n = x.atoms[atomTargets], n+1
		if st.wlUTF8 {
			list[n], n = x.atoms[atomUTF8String], n+1
			list[n], n = x.atoms[atomText], n+1
		}
		list[n], n = x.atoms[atomString], n+1
		xproto.ChangeProperty(x.conn.Buf(), xproto.PropModeReplace, xproto.Window(req), xproto.Atom(prop),
			xproto.AtomAtom, 32, uint32(n), x.encode32(list[:n]))
		x.notify(req, sel, target, prop, time)
		return
	case x.atoms[atomUTF8String], x.atoms[atomText], x.atoms[atomString]:
	default:
		x.notify(req, sel, target, 0, time)
		return
	}
	// Choose the Wayland MIME: UTF-8 targets prefer the UTF-8 types; STRING
	// prefers plain text and falls back to the UTF-8 bytes unchanged.
	mime := ""
	order := [...]int{0, 2, 4, 1, 3} // utf8 types, then plain
	if target == x.atoms[atomString] {
		order = [...]int{1, 3, 0, 2, 4}
	}
	for _, i := range order {
		if st.wlMime[i] != "" && (target == x.atoms[atomString] || mimeTarget[i] == atomUTF8String) {
			mime = st.wlMime[i]
			break
		}
	}
	if mime == "" {
		x.notify(req, sel, target, 0, time)
		return
	}
	for i := range x.sel.out { // one transfer per (requestor, property)
		o := &x.sel.out[i]
		if o.state != outFree && o.requestor == req && o.property == prop {
			x.notify(req, sel, target, 0, time)
			return
		}
	}
	slot := -1
	for i := range x.sel.out {
		if x.sel.out[i].state == outFree {
			slot = i
			break
		}
	}
	if slot < 0 || x.sel.pool.n == 0 {
		x.log.Warn().Msg("selection: too many transfers, request refused")
		x.notify(req, sel, target, 0, time)
		return
	}
	fd := x.host.SelectionRead(k, mime)
	if fd < 0 {
		x.notify(req, sel, target, 0, time)
		return
	}
	x.startOut(slot, k, req, prop, target, time, fd)
}

// startOut begins serving the data on fd (a non-blocking read end it now
// owns) to requestor.
func (x *XWM) startOut(slot, k int, req, prop, target, time uint32, fd int) {
	o := &x.sel.out[slot]
	o.state, o.kind, o.requestor, o.property, o.target, o.time = outActive, k, req, prop, target, time
	o.fd, o.buf, o.n, o.eof, o.incr, o.credit, o.notified, o.idle, o.src = fd, x.sel.pool.get(), 0, false, false, false, false, 0, nil
	x.selEnsureTimer()
	x.outRead(slot) // data may already be waiting
}

// encode32 packs vals into the shared scratch.
func (x *XWM) encode32(vals []uint32) []byte {
	n := 4 * len(vals)
	if cap(x.scratch) < n {
		x.scratch = make([]byte, 0, n)
	}
	p := x.scratch[:n]
	for i, v := range vals {
		binary.LittleEndian.PutUint32(p[4*i:], v)
	}
	return p
}

// outWatch registers the pipe's read end with the display.
func (x *XWM) outWatch(i int) {
	o := &x.sel.out[i]
	if o.src != nil {
		return
	}
	if x.lp == nil {
		x.outAbort(i)
		return
	}
	src, err := x.lp.AddFD(o.fd, o.onR, nil)
	if err != nil {
		x.outAbort(i)
		return
	}
	o.src = src
}

// outUnwatch drops read interest: the buffer is full and nothing may be read
// until the requestor consumed the previous chunk.
func (x *XWM) outUnwatch(i int) {
	o := &x.sel.out[i]
	if o.src != nil {
		o.src.Remove()
		o.src = nil
	}
}

// outReadable is the pipe's read callback.
func (x *XWM) outReadable(i int) {
	if x.sel.out[i].state == outFree {
		return
	}
	x.outRead(i)
}

// outRead fills the buffer from the pipe and advances the transfer.
func (x *XWM) outRead(i int) {
	o := &x.sel.out[i]
	buf := &x.sel.pool.bufs[o.buf]
	for o.n < chunk && !o.eof {
		n, err := unix.Read(o.fd, buf[o.n:])
		switch {
		case err == nil && n == 0:
			o.eof = true
		case err == nil:
			o.n += n
			o.idle = 0
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN):
			x.outPump(i)
			return
		default:
			x.outAbort(i)
			return
		}
	}
	x.outPump(i)
}

// outPump sends what the state allows.
func (x *XWM) outPump(i int) {
	o := &x.sel.out[i]
	if o.state == outFree {
		return
	}
	full := o.n == chunk
	if !o.incr {
		switch {
		case o.eof: // whole value
			x.outSetProp(i, o.n)
			x.notify(o.requestor, x.sel.st[o.kind].atom, o.target, o.property, o.time)
			x.outFinish(i)
		case full: // does not fit one request: INCR
			o.incr, o.notified = true, true
			var v xproto.ChangeWindowAttributesValues
			v.SetEventMask(xproto.EventMaskPropertyChange)
			xproto.ChangeWindowAttributes(x.conn.Buf(), xproto.Window(o.requestor), &v)
			xproto.ChangeProperty(x.conn.Buf(), xproto.PropModeReplace, xproto.Window(o.requestor), xproto.Atom(o.property),
				xproto.Atom(x.atoms[atomIncr]), 32, 1, x.encode32([]uint32{0}))
			x.notify(o.requestor, x.sel.st[o.kind].atom, o.target, o.property, o.time)
			x.outUnwatch(i)
		default:
			x.outWatch(i)
		}
		return
	}
	if o.credit && (full || o.eof) {
		o.credit = false
		o.idle = 0
		x.outSetProp(i, o.n)
		if o.n == 0 { // the empty chunk ends the transfer
			x.outFinish(i)
			return
		}
		o.n = 0
	}
	if o.n < chunk && !o.eof {
		x.outWatch(i)
	} else {
		x.outUnwatch(i)
	}
}

// outSetProp writes the first n buffered bytes as the property value.
func (x *XWM) outSetProp(i, n int) {
	o := &x.sel.out[i]
	seq := xproto.ChangeProperty(x.conn.Buf(), xproto.PropModeReplace, xproto.Window(o.requestor), xproto.Atom(o.property),
		xproto.Atom(o.target), 8, uint32(n), x.sel.pool.bufs[o.buf][:n])
	if seq != 0 {
		if err := x.conn.Expect(seq, kindSelOut, uint32(i), o.gen, 0, 0); err != nil {
			x.stats.RingFull++
		}
	}
	x.wake()
}

// outFinish ends a completed transfer.
func (x *XWM) outFinish(i int) {
	o := &x.sel.out[i]
	if o.incr && !x.closed {
		var v xproto.ChangeWindowAttributesValues
		v.SetEventMask(0)
		xproto.ChangeWindowAttributes(x.conn.Buf(), xproto.Window(o.requestor), &v)
		x.wake()
	}
	x.outRelease(i)
}

// outRelease frees the transfer's resources.
func (x *XWM) outRelease(i int) {
	o := &x.sel.out[i]
	x.outUnwatch(i)
	if o.fd >= 0 {
		_ = unix.Close(o.fd)
	}
	if o.buf >= 0 {
		x.sel.pool.put(o.buf)
	}
	o.fd, o.buf = -1, -1
	o.gen++
	o.state = outFree
}

// outAbort ends a transfer early: the requestor is refused (before its
// notification) or released (INCR: mask dropped, property deleted).
func (x *XWM) outAbort(i int) {
	o := &x.sel.out[i]
	if o.state == outFree {
		return
	}
	if !x.closed {
		switch {
		case !o.notified:
			x.notify(o.requestor, x.sel.st[o.kind].atom, o.target, 0, o.time)
		case o.incr:
			var v xproto.ChangeWindowAttributesValues
			v.SetEventMask(0)
			xproto.ChangeWindowAttributes(x.conn.Buf(), xproto.Window(o.requestor), &v)
			xproto.DeleteProperty(x.conn.Buf(), xproto.Window(o.requestor), xproto.Atom(o.property))
			x.wake()
		}
	}
	x.outRelease(i)
}

// onRequestorProperty: PropertyNotify on a window we serve by INCR.
func (x *XWM) onRequestorProperty(e xproto.PropertyNotifyEvent) bool {
	if e.State() != xproto.PropertyDelete {
		return false
	}
	hit := false
	for i := range x.sel.out {
		o := &x.sel.out[i]
		if o.state == outFree || !o.incr || o.requestor != uint32(e.Window()) || o.property != uint32(e.Atom()) {
			continue
		}
		hit = true
		o.credit = true
		o.idle = 0
		x.outPump(i)
	}
	return hit
}
