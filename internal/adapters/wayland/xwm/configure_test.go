package xwm

import (
	"encoding/binary"
	"github.com/bnema/neferwl/internal/domain"
	"testing"
	"time"

	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newWin makes a ready managed window known to the XWM and the mocked host.
func newWin(t *testing.T, x *XWM, p *xpeer, h *mockhost, xid uint32) Slot {
	t.Helper()
	h.EXPECT().Created(mock.Anything, xid).Return(domain.WindowID(xid)).Once()
	p.createNotify(xid, false)
	readUntil(t, x, func() bool { return x.tab.lookup(xid) != 0 })
	s := x.tab.lookup(xid)
	r := x.tab.rec(s)
	r.ready, r.w, r.h = true, 300, 200
	return s
}

// settle flushes the XWM and waits until the peer has seen everything sent so
// far; it returns the number of requests received.
func settle(t *testing.T, x *XWM, p *xpeer) int {
	t.Helper()
	x.flush()
	n := len(p.requests())
	xproto.GetInputFocus(x.conn.Buf())
	require.NoError(t, x.conn.Flush())
	_, n = p.waitReq(n, func(r xpeerReq) bool { return r.major == opGetInputFocus })
	return n
}

// since returns the requests of the given major opcode received after skip.
func since(p *xpeer, skip int, major uint8) []xpeerReq {
	var out []xpeerReq
	for _, r := range p.requests()[skip:] {
		if r.major == major {
			out = append(out, r)
		}
	}
	return out
}

// sentEvents returns the 32-byte events of the SendEvent requests to dest.
func sentEvents(p *xpeer, skip int, dest uint32) [][]byte {
	var out [][]byte
	for _, r := range since(p, skip, opSendEvent) {
		if r.u32(4) == dest {
			out = append(out, r.raw[12:44])
		}
	}
	return out
}

func propsTo(p *xpeer, skip int, win uint32, atom string) []xpeerReq {
	var out []xpeerReq
	a := p.atom(atom)
	for _, r := range since(p, skip, opChangeProperty) {
		if r.u32(4) == win && r.u32(8) == a {
			out = append(out, r)
		}
	}
	return out
}

func (x *XWM) feed(t *testing.T, p *xpeer, send func()) {
	t.Helper()
	ev := x.stats.Events
	send()
	readUntil(t, x, func() bool { return x.stats.Events > ev })
}

func TestConfigureCoalescesIntoOneRequest(t *testing.T) {
	x, p, h, _, runs := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	n := settle(t, x, p)

	x.Configure(s, ports.ConfigureWindow{Width: 640, Height: 480})
	x.Configure(s, ports.ConfigureWindow{Width: 800, Height: 600, Activated: true})
	x.Place(s, 10, 20)
	require.Len(t, *runs, 1, "one flush posted")
	n2 := settle(t, x, p)

	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.Equal(t, uint32(win), cw[0].u32(4))
	require.EqualValues(t, 0x1f, binary.LittleEndian.Uint16(cw[0].raw[8:]), "x y width height border")
	require.Equal(t, []uint32{10, 20, 800, 600, 0}, []uint32{cw[0].u32(12), cw[0].u32(16), cw[0].u32(20), cw[0].u32(24), cw[0].u32(28)})

	evs := sentEvents(p, n, win)
	require.Len(t, evs, 1, "one synthetic ConfigureNotify")
	require.EqualValues(t, 22, evs[0][0])
	require.EqualValues(t, win, binary.LittleEndian.Uint32(evs[0][4:]))
	require.EqualValues(t, win, binary.LittleEndian.Uint32(evs[0][8:]))
	require.EqualValues(t, 10, binary.LittleEndian.Uint16(evs[0][16:]))
	require.EqualValues(t, 20, binary.LittleEndian.Uint16(evs[0][18:]))
	require.EqualValues(t, 800, binary.LittleEndian.Uint16(evs[0][20:]))
	require.EqualValues(t, 600, binary.LittleEndian.Uint16(evs[0][22:]))
	require.Equal(t, xproto.EventMaskStructureNotify, xproto.EventMask(since(p, n, opSendEvent)[0].u32(8)))

	// Unchanged: nothing is sent.
	x.Configure(s, ports.ConfigureWindow{Width: 800, Height: 600, Activated: true})
	x.Place(s, 10, 20)
	settle(t, x, p)
	require.Len(t, since(p, n2, opConfigureWindow), 0)
	require.Len(t, sentEvents(p, n2, win), 0)
}

func TestConfigureWritesNetWMState(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	x.tab.rec(s).modal = true
	n := settle(t, x, p)

	x.Configure(s, ports.ConfigureWindow{Width: 100, Height: 100})
	settle(t, x, p)
	require.Empty(t, propsTo(p, n, win, "_NET_WM_STATE"), "no state bit changed")

	x.Configure(s, ports.ConfigureWindow{Width: 100, Height: 100, Fullscreen: true, Activated: true})
	settle(t, x, p)
	st, ok := p.prop(win, "_NET_WM_STATE")
	require.True(t, ok)
	require.ElementsMatch(t, []uint32{p.atom("_NET_WM_STATE_MODAL"), p.atom("_NET_WM_STATE_FULLSCREEN"), p.atom("_NET_WM_STATE_FOCUSED")}, atomsOf(st))

	x.Configure(s, ports.ConfigureWindow{Width: 100, Height: 100, Activated: true})
	settle(t, x, p)
	st, _ = p.prop(win, "_NET_WM_STATE")
	require.ElementsMatch(t, []uint32{p.atom("_NET_WM_STATE_MODAL"), p.atom("_NET_WM_STATE_FOCUSED")}, atomsOf(st))
}

func TestConfigureBadWindowIsAttributedAndIgnored(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	n := settle(t, x, p)
	x.Configure(s, ports.ConfigureWindow{Width: 640, Height: 480})
	x.flush()
	cw := []xpeerReq{}
	r, _ := p.waitReq(n, func(r xpeerReq) bool { return r.major == opConfigureWindow })
	cw = append(cw, r)

	p.mu.Lock()
	e := make([]byte, 32)
	e[1] = xproto.BadWindow
	binary.LittleEndian.PutUint16(e[2:], cw[0].seq)
	binary.LittleEndian.PutUint32(e[4:], win)
	e[10] = opConfigureWindow
	p.write(e)
	p.mu.Unlock()
	readUntil(t, x, func() bool { return x.stats.Errors == 1 })
	require.Zero(t, x.stats.Unattributed)
	require.False(t, x.closed)
}

func sendConfigureRequest(p *xpeer, win uint32, mask uint16, x, y int16, w, h uint16) {
	p.event(23, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
		binary.LittleEndian.PutUint16(b[16:], uint16(x))
		binary.LittleEndian.PutUint16(b[18:], uint16(y))
		binary.LittleEndian.PutUint16(b[20:], w)
		binary.LittleEndian.PutUint16(b[22:], h)
		binary.LittleEndian.PutUint16(b[26:], mask)
	})
}

func TestConfigureRequestTiledOnlyGetsNotify(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	newWin(t, x, p, h, win)
	n := settle(t, x, p)

	x.feed(t, p, func() { sendConfigureRequest(p, win, 0xf, 5, 6, 1000, 900) })
	settle(t, x, p)
	require.Empty(t, since(p, n, opConfigureWindow), "a tiled window cannot resize itself")
	evs := sentEvents(p, n, win)
	require.Len(t, evs, 1)
	require.EqualValues(t, 22, evs[0][0])
	require.EqualValues(t, 300, binary.LittleEndian.Uint16(evs[0][20:]), "current geometry")
	require.EqualValues(t, 200, binary.LittleEndian.Uint16(evs[0][22:]))
}

func TestConfigureRequestFloatingHonoursSize(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	x.tab.rec(s).floating = true
	n := settle(t, x, p)

	x.feed(t, p, func() {
		sendConfigureRequest(p, win, uint16(xproto.ConfigWindowWidth|xproto.ConfigWindowHeight), 0, 0, 70000&0xffff, 20000)
	})
	settle(t, x, p)
	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.EqualValues(t, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, binary.LittleEndian.Uint16(cw[0].raw[8:]))
	require.EqualValues(t, 4464, cw[0].u32(12))
	require.EqualValues(t, maxClientSize, cw[0].u32(16), "clamped")
	require.Len(t, sentEvents(p, n, win), 1)
}

func TestConfigureRequestUnmanagedHonouredVerbatim(t *testing.T) {
	x, p, _, _, _ := attached(t)
	const win = 0x500009
	p.createNotify(win, true)
	readUntil(t, x, func() bool { return x.tab.lookup(win) != 0 })
	n := settle(t, x, p)

	x.feed(t, p, func() {
		sendConfigureRequest(p, win, uint16(xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth), 30, 40, 50, 60)
	})
	settle(t, x, p)
	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.EqualValues(t, 7, binary.LittleEndian.Uint16(cw[0].raw[8:]))
	require.Equal(t, []uint32{30, 40, 50}, []uint32{cw[0].u32(12), cw[0].u32(16), cw[0].u32(20)})
	require.Empty(t, sentEvents(p, n, win))
}

func TestConfigureStaleAndDuplicateDirtySlots(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const a, b = 0x500001, 0x500002
	s := newWin(t, x, p, h, a)
	for i := 1; i <= 50; i++ {
		x.Configure(s, ports.ConfigureWindow{Width: 100 + i, Height: 100})
	}
	require.Len(t, x.dirty, 1, "queued once per false→true transition")

	// The window dies with its entry queued; the slot is reused.
	h.EXPECT().Dissociate(s, true).Once()
	p.destroyNotify(a)
	readUntil(t, x, func() bool { return x.tab.lookup(a) == 0 })
	s2 := newWin(t, x, p, h, b)
	require.Equal(t, s, s2, "LIFO slot reuse")
	n := len(p.requests())
	x.Configure(s2, ports.ConfigureWindow{Width: 640, Height: 480})
	require.Len(t, x.dirty, 1)
	x.dirty = append(x.dirty, s, s) // stale and duplicate entries must be skipped
	settle(t, x, p)
	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1, "sent once despite duplicates")
	require.Equal(t, uint32(b), cw[0].u32(4))
	require.Empty(t, x.dirty)
	require.LessOrEqual(t, cap(x.dirty), len(x.tab.recs))
}

func TestDirtyQueueNeverGrows(t *testing.T) {
	x, p, h, src, _ := attached(t)
	// Hundreds of requests can fill the socket on a single-P runner; writable
	// interest is normal transport backpressure, not part of the queue invariant.
	src.EXPECT().SetWritable(mock.Anything).Return(nil).Maybe()
	c0 := cap(x.dirty)
	const base = 0x600000
	for i := 0; i < 3*c0; i++ { // each round leaves a stale entry behind
		xid := uint32(base + i)
		h.EXPECT().Created(mock.Anything, xid).Return(domain.WindowID(1)).Once()
		p.createNotify(xid, false)
		readUntil(t, x, func() bool { return x.tab.lookup(xid) != 0 })
		s := x.tab.lookup(xid)
		x.tab.rec(s).ready = true
		x.Configure(s, ports.ConfigureWindow{Width: 10, Height: 10})
		h.EXPECT().Dissociate(s, true).Once()
		p.destroyNotify(xid)
		readUntil(t, x, func() bool { return x.tab.lookup(xid) == 0 })
	}
	require.Equal(t, c0, cap(x.dirty))
	require.LessOrEqual(t, len(x.dirty), c0)
}

func TestPublicCommandsIgnoredWhenClosed(t *testing.T) {
	x, p, h, src, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	h.EXPECT().Dissociate(s, true).Once()
	src.EXPECT().Remove().Once()
	for kind := range x.sel.st {
		h.EXPECT().SelectionOwned(kind, []string(nil)).Once()
	}
	x.Close()
	require.NotPanics(t, func() {
		x.Configure(s, ports.ConfigureWindow{Width: 1, Height: 1})
		x.Place(s, 1, 2)
		x.Focus(s)
		x.CloseWindow(s)
	})
	require.Empty(t, x.dirty)
	require.Zero(t, x.active)
}

// Focus.

type focusModel struct {
	name        string
	input, take bool
	setFocus    bool // SetInputFocus on the window
	takeFocus   bool // WM_TAKE_FOCUS sent
}

func TestFocusInputModels(t *testing.T) {
	for _, m := range []focusModel{
		{"passive", true, false, true, false},
		{"locally active", true, true, true, true},
		{"globally active", false, true, false, true},
		{"no input", false, false, false, false},
	} {
		t.Run(m.name, func(t *testing.T) {
			x, p, h, _, _ := attached(t)
			const win = 0x500001
			s := newWin(t, x, p, h, win)
			r := x.tab.rec(s)
			r.input, r.takeFocus = m.input, m.take
			n := settle(t, x, p)

			x.Focus(s)
			settle(t, x, p)
			var focuses []xpeerReq
			for _, r := range since(p, n, opSetInputFocus) {
				if r.u32(4) == win {
					focuses = append(focuses, r)
				}
			}
			if m.setFocus {
				require.Len(t, focuses, 1)
				require.EqualValues(t, xproto.InputFocusNone, focuses[0].raw[1], "never PointerRoot")
				require.Zero(t, focuses[0].u32(8), "CurrentTime")
			} else {
				require.Empty(t, focuses)
			}
			var takes int
			for _, ev := range sentEvents(p, n, win) {
				if ev[0] == 33 && binary.LittleEndian.Uint32(ev[8:]) == p.atom("WM_PROTOCOLS") {
					require.EqualValues(t, 32, ev[1])
					require.Equal(t, p.atom("WM_TAKE_FOCUS"), binary.LittleEndian.Uint32(ev[12:]))
					require.Zero(t, binary.LittleEndian.Uint32(ev[16:]), "CurrentTime")
					takes++
				}
			}
			require.Equal(t, map[bool]int{true: 1, false: 0}[m.takeFocus], takes)

			act, ok := p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
			require.True(t, ok)
			require.Equal(t, []uint32{win}, atomsOf(act), "named even for No-input windows")
			st, ok := p.prop(win, "_NET_WM_STATE")
			require.True(t, ok)
			require.Contains(t, atomsOf(st), p.atom("_NET_WM_STATE_FOCUSED"))
		})
	}
}

func TestFocusActiveWindowWrittenOnlyOnChange(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const a, b = 0x500001, 0x500002
	sa, sb := newWin(t, x, p, h, a), newWin(t, x, p, h, b)
	n := settle(t, x, p)

	x.Focus(sa)
	x.Focus(sa)
	settle(t, x, p)
	require.Len(t, propsTo(p, n, peerRoot, "_NET_ACTIVE_WINDOW"), 1)

	x.Focus(sb)
	settle(t, x, p)
	act, _ := p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
	require.Equal(t, []uint32{b}, atomsOf(act))
	sta, _ := p.prop(a, "_NET_WM_STATE")
	require.NotContains(t, atomsOf(sta), p.atom("_NET_WM_STATE_FOCUSED"), "old window loses FOCUSED")
	stb, _ := p.prop(b, "_NET_WM_STATE")
	require.Contains(t, atomsOf(stb), p.atom("_NET_WM_STATE_FOCUSED"))

	n = settle(t, x, p)
	x.Focus(0)
	settle(t, x, p)
	none := since(p, n, opSetInputFocus)
	require.Len(t, none, 1)
	require.Zero(t, none[0].u32(4), "focus None")
	require.EqualValues(t, xproto.InputFocusNone, none[0].raw[1])
	act, _ = p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
	require.Equal(t, []uint32{0}, atomsOf(act))
	stb, _ = p.prop(b, "_NET_WM_STATE")
	require.NotContains(t, atomsOf(stb), p.atom("_NET_WM_STATE_FOCUSED"))
	require.Zero(t, x.active)
}

func TestFocusStackingLists(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const a, b, c = 0x500001, 0x500002, 0x500003
	sa := newWin(t, x, p, h, a)
	newWin(t, x, p, h, b)
	newWin(t, x, p, h, c)
	for _, w := range []uint32{a, b, c} {
		x.addClient(w)
	}
	settle(t, x, p)
	list, _ := p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Equal(t, []uint32{a, b, c}, atomsOf(list))
	n := settle(t, x, p)

	x.Focus(sa)
	settle(t, x, p)
	stack, _ := p.prop(peerRoot, "_NET_CLIENT_LIST_STACKING")
	require.Equal(t, []uint32{b, c, a}, atomsOf(stack), "bottom to top")
	list, _ = p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Equal(t, []uint32{a, b, c}, atomsOf(list), "map order")
	require.Empty(t, propsTo(p, n, peerRoot, "_NET_CLIENT_LIST"), "clients list untouched by focus")
	var raised bool
	for _, r := range since(p, n, opConfigureWindow) {
		if r.u32(4) == a && binary.LittleEndian.Uint16(r.raw[8:]) == uint16(xproto.ConfigWindowStackMode) {
			require.EqualValues(t, xproto.StackModeAbove, r.u32(12))
			raised = true
		}
	}
	require.True(t, raised)

	// A burst costs one write per list.
	n = settle(t, x, p)
	x.removeClient(b)
	x.removeClient(c)
	settle(t, x, p)
	require.Len(t, propsTo(p, n, peerRoot, "_NET_CLIENT_LIST"), 1)
	require.Len(t, propsTo(p, n, peerRoot, "_NET_CLIENT_LIST_STACKING"), 1)
	stack, _ = p.prop(peerRoot, "_NET_CLIENT_LIST_STACKING")
	require.Equal(t, []uint32{a}, atomsOf(stack))
}

func TestDestroyedActiveWindowClearsActive(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	x.addClient(win)
	x.Focus(s)
	settle(t, x, p)
	h.EXPECT().Dissociate(s, true).Once()
	p.destroyNotify(win)
	readUntil(t, x, func() bool { return x.tab.lookup(win) == 0 })
	settle(t, x, p)
	require.Zero(t, x.active)
	act, _ := p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
	require.Equal(t, []uint32{0}, atomsOf(act))
	list, _ := p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Empty(t, list.data)
}

func TestFocusInDenied(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const a, b = 0x500001, 0x500002
	sa := newWin(t, x, p, h, a)
	newWin(t, x, p, h, b)
	x.Focus(sa)
	n := settle(t, x, p)

	focusIn := func(win uint32, detail byte) {
		p.event(9, func(e *[32]byte) {
			e[1] = detail
			binary.LittleEndian.PutUint32(e[4:], win)
		})
	}
	focusCount := func(skip int) int {
		c := 0
		for _, r := range since(p, skip, opSetInputFocus) {
			if r.u32(4) == a {
				c++
			}
		}
		return c
	}
	x.feed(t, p, func() { focusIn(a, 0) }) // the active window itself: fine
	x.feed(t, p, func() { focusIn(b, byte(xproto.NotifyDetailPointer)) })
	settle(t, x, p)
	require.Zero(t, focusCount(n))

	// Both events must be dispatched: the old absolute threshold of five
	// also included the two creates and could pass after just one FocusIn.
	before := x.stats.Events
	focusIn(b, 0)
	focusIn(b, 0)
	readUntil(t, x, func() bool { return x.stats.Events == before+2 })
	pumpReq(t, x, p, n, func(r xpeerReq) bool {
		return r.major == opSetInputFocus && r.u32(4) == a
	})
	settle(t, x, p)
	require.Equal(t, 1, focusCount(n), "denied once per iteration")
	n = settle(t, x, p)
	settle(t, x, p)
	require.Zero(t, focusCount(n))
}

func TestCloseWindow(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const a, b = 0x500001, 0x500002
	sa, sb := newWin(t, x, p, h, a), newWin(t, x, p, h, b)
	x.tab.rec(sa).deleteWindow = true
	n := settle(t, x, p)

	x.CloseWindow(sa)
	x.CloseWindow(sb)
	settle(t, x, p)
	evs := sentEvents(p, n, a)
	require.Len(t, evs, 1)
	require.EqualValues(t, 33, evs[0][0])
	require.EqualValues(t, 32, evs[0][1])
	require.Equal(t, p.atom("WM_PROTOCOLS"), binary.LittleEndian.Uint32(evs[0][8:]))
	require.Equal(t, p.atom("WM_DELETE_WINDOW"), binary.LittleEndian.Uint32(evs[0][12:]))
	require.Zero(t, binary.LittleEndian.Uint32(evs[0][16:]))
	require.Empty(t, sentEvents(p, n, b))

	kills := since(p, n, opKillClient)
	require.Len(t, kills, 1)
	require.Equal(t, uint32(b), kills[0].u32(4))
}

// Allocation guards. The peer sinks requests so the connection never blocks;
// its goroutine is idle in the sink loop before measuring.

//testpolicy:realtime waits for the real X11 peer to consume a sink marker
func sinkPeer(t *testing.T, x *XWM, p *xpeer) {
	t.Helper()
	// Everything written after sink.Store(true) is discarded, so one marker
	// request proves the peer loop is in sink mode and idle again.
	// A round trip first: the peer has consumed every earlier request, so the
	// marker is the only one it can see in sink mode.
	require.NoError(t, x.conn.Sync(&x.h))
	before := p.discarded.Load()
	p.sink.Store(true)
	xproto.GetInputFocus(x.conn.Buf())
	require.NoError(t, x.conn.Flush())
	for p.discarded.Load() == before {
		select {
		case <-p.discardedEvent:
		case <-time.After(5 * time.Second):
			t.Fatal("sink marker never discarded")
		}
	}

	x.src = nil // no source: write backpressure would not allocate either
	x.post = func(func()) {}
	x.warnedRing = true // the sink never answers: Expect slots stay used
}

func TestConfigureFlushAllocations(t *testing.T) {
	x, p, h, _, _ := attached(t)
	s := newWin(t, x, p, h, 0x500001)
	sinkPeer(t, x, p)
	i := 0
	allocs := testing.AllocsPerRun(300, func() {
		i++
		x.Configure(s, ports.ConfigureWindow{Width: 100 + i%2, Height: 100, Fullscreen: i%3 == 0, Activated: i%2 == 0})
		x.Place(s, i%5, i%7)
		x.posted = false
		x.flush()
	})
	require.Zero(t, allocs)
	require.False(t, x.closed)
}

func TestFocusAllocations(t *testing.T) {
	x, p, h, _, _ := attached(t)
	a, b := newWin(t, x, p, h, 0x500001), newWin(t, x, p, h, 0x500002)
	x.addClient(x.tab.rec(a).xid)
	x.addClient(x.tab.rec(b).xid)
	x.tab.rec(b).takeFocus = true
	x.tab.rec(a).input, x.tab.rec(b).input = true, true
	sinkPeer(t, x, p)
	i := 0
	allocs := testing.AllocsPerRun(150, func() {
		i++
		switch i % 3 {
		case 0:
			x.Focus(a)
		case 1:
			x.Focus(b)
		default:
			x.Focus(0)
		}
		x.refocus = i%5 == 0
		x.posted = false
		x.flush()
	})
	require.Zero(t, allocs)
	require.False(t, x.closed)
}

// Repeated identical fullscreen configures cost nothing.
func TestFullscreenConfigureAllocations(t *testing.T) {
	x, p, h, _, _ := attached(t)
	s := newWin(t, x, p, h, 0x500001)
	sinkPeer(t, x, p)
	cfg := ports.ConfigureWindow{Width: 1920, Height: 1080, Fullscreen: true, Activated: true}
	x.Configure(s, cfg)
	x.posted = false
	x.flush()
	allocs := testing.AllocsPerRun(300, func() {
		x.Configure(s, cfg)
		x.Place(s, 0, 0)
		x.posted = false
		x.flush()
	})
	require.Zero(t, allocs)
}
