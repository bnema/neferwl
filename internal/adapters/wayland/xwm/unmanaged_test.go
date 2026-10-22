package xwm

import (
	"encoding/binary"
	"testing"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func layoutOf(os ...ports.OutputPlacement) ports.Layout { return ports.Layout(os) }

func rootConfigureNotify(win uint32, x, y int16, w, h uint16) xturbo.Event {
	ev := xturbo.Event{Kind: xproto.KindConfigureNotify}
	binary.LittleEndian.PutUint32(ev.Raw[4:], peerRoot)
	binary.LittleEndian.PutUint32(ev.Raw[8:], win)
	binary.LittleEndian.PutUint16(ev.Raw[16:], uint16(x))
	binary.LittleEndian.PutUint16(ev.Raw[18:], uint16(y))
	binary.LittleEndian.PutUint16(ev.Raw[20:], w)
	binary.LittleEndian.PutUint16(ev.Raw[22:], h)
	return ev
}

func (p *xpeer) rootConfigureNotify(win uint32, x, y int16, w, h uint16) {
	p.event(22, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
		binary.LittleEndian.PutUint16(b[16:], uint16(x))
		binary.LittleEndian.PutUint16(b[18:], uint16(y))
		binary.LittleEndian.PutUint16(b[20:], w)
		binary.LittleEndian.PutUint16(b[22:], h)
	})
}

func TestPlaceConvertsLayoutToXCoordinates(t *testing.T) {
	x, p, h, _, _ := attached(t)
	s := newWin(t, x, p, h, 0x500001)
	x.SetLayout(layoutOf(out(-640, 0, 640, 480), out(0, 0, 1920, 1080)))
	n := settle(t, x, p)

	x.Place(s, -640+10, 20)
	settle(t, x, p)
	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.EqualValues(t, 10, cw[0].u32(12))
	require.EqualValues(t, 20, cw[0].u32(16))
}

func TestSetLayoutRepositionsPlacedWindows(t *testing.T) {
	x, p, h, _, _ := attached(t)
	s := newWin(t, x, p, h, 0x500001)
	x.SetLayout(layoutOf(out(0, 0, 1920, 1080)))
	x.Place(s, 100, 50)
	n := settle(t, x, p)

	x.SetLayout(layoutOf(out(0, 0, 1920, 1080))) // unchanged: nothing resent
	settle(t, x, p)
	require.Empty(t, since(p, n, opConfigureWindow))

	x.SetLayout(layoutOf(out(-100, 0, 100, 100), out(0, 0, 1920, 1080)))
	settle(t, x, p)
	cw := since(p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.EqualValues(t, 200, cw[0].u32(12))
	require.EqualValues(t, 50, cw[0].u32(16))
}

func TestSetLayoutWarnsOnceWhenOutputsDropped(t *testing.T) {
	x, _, _, _, _ := attached(t)
	var l ports.Layout
	for i := 0; i < maxRegions+2; i++ {
		l = append(l, out(i*100, 0, 100, 100))
	}
	x.SetLayout(l)
	require.True(t, x.warnedSpace)
	require.Equal(t, 2, x.space.Dropped())
}

// overrideWindow creates and maps an override window, answers its type read
// and pairs it; the mocked host must expect OverrideMapped beforehand.
func overrideWindow(t *testing.T, x *XWM, p *xpeer, win uint32, typ string) Slot {
	t.Helper()
	if typ != "" {
		p.setProp(win, p.atom("_NET_WM_WINDOW_TYPE"), uint32(xproto.AtomAtom), 32, le32(p.atom(typ)))
	}
	p.createNotify(win, true)
	readUntil(t, x, func() bool { return x.tab.lookup(win) != 0 })
	s := x.tab.lookup(win)
	x.feed(t, p, func() { p.rootConfigureNotify(win, 30, 40, 100, 50) })
	x.feed(t, p, func() { p.mapNotify(win) })
	return s
}

func TestUnmanagedMapReadsTypeThenPairsThenReports(t *testing.T) {
	x, p, h, _, _ := attached(t)
	x.SetLayout(layoutOf(out(-640, 0, 640, 480), out(0, 0, 1920, 1080)))
	const win = 0x500009
	s := overrideWindow(t, x, p, win, "_NET_WM_WINDOW_TYPE_TOOLTIP")
	x.flush()
	readUntil(t, x, func() bool { return x.tab.rec(s).ready })
	typeReads := 0
	for _, q := range p.requests() {
		if q.major == opGetProperty && q.u32(4) == win {
			typeReads++
		}
	}
	require.Equal(t, 1, typeReads)

	h.EXPECT().OverrideMapped(s, uint32(win), uint64(7), -640+30, 40, 100, 50, true).Once()
	x.SetSerial(7, 9)
	x.feed(t, p, func() { p.clientMessage(win, p.atom("WL_SURFACE_SERIAL"), 9, 0) })
	require.True(t, x.tab.rec(s).ovPaired)
}

func TestUnmanagedDefaultTypeTakesInput(t *testing.T) {
	x, p, h, _, _ := attached(t)
	x.SetLayout(layoutOf(out(0, 0, 1920, 1080)))
	const win = 0x500009
	s := overrideWindow(t, x, p, win, "_NET_WM_WINDOW_TYPE_POPUP_MENU")
	x.SetSerial(7, 9)
	h.EXPECT().OverrideMapped(s, uint32(win), uint64(7), 30, 40, 100, 50, false).Once()
	x.feed(t, p, func() { p.clientMessage(win, p.atom("WL_SURFACE_SERIAL"), 9, 0) })
	readUntil(t, x, func() bool { return x.tab.rec(s).ovPaired })
}

func TestUnmanagedMoveAndLayoutChangeAndUnmap(t *testing.T) {
	x, p, h, _, _ := attached(t)
	x.SetLayout(layoutOf(out(0, 0, 1920, 1080)))
	const win = 0x500009
	s := overrideWindow(t, x, p, win, "")
	x.SetSerial(7, 9)
	h.EXPECT().OverrideMapped(s, uint32(win), uint64(7), 30, 40, 100, 50, false).Once()
	x.feed(t, p, func() { p.clientMessage(win, p.atom("WL_SURFACE_SERIAL"), 9, 0) })
	readUntil(t, x, func() bool { return x.tab.rec(s).ovPaired })

	h.EXPECT().OverrideMoved(s, 35, 45, 100, 60).Once()
	x.feed(t, p, func() { p.rootConfigureNotify(win, 35, 45, 100, 60) })
	x.flush()
	x.feed(t, p, func() { p.rootConfigureNotify(win, 35, 45, 100, 60) }) // same rect: no call
	x.flush()

	h.EXPECT().OverrideMoved(s, -65, 45, 100, 60).Once() // the layout origin moved: same X point, new layout point
	x.SetLayout(layoutOf(out(-100, 0, 100, 100), out(0, 0, 1920, 1080)))
	x.flush()

	h.EXPECT().OverrideGone(s).Once()
	x.feed(t, p, func() { p.unmapNotify(win, false) })
	require.False(t, x.tab.rec(s).ovPaired)
}

func TestUnmanagedDestroyReportsGone(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500009
	s := overrideWindow(t, x, p, win, "")
	x.SetSerial(7, 9)
	h.EXPECT().OverrideMapped(s, uint32(win), uint64(7), 30, 40, 100, 50, false).Once()
	x.feed(t, p, func() { p.clientMessage(win, p.atom("WL_SURFACE_SERIAL"), 9, 0) })
	readUntil(t, x, func() bool { return x.tab.rec(s).ovPaired })
	h.EXPECT().OverrideGone(s).Once()
	x.feed(t, p, func() { p.destroyNotify(win) })
	require.Zero(t, x.tab.lookup(win))
}

func TestFocusInIgnoresUnmanagedAndGrabModes(t *testing.T) {
	x, p, h, _, _ := attached(t)
	sa := newWin(t, x, p, h, 0x500001)
	newWin(t, x, p, h, 0x500002)
	x.Focus(sa)
	p.createNotify(0x500009, true)
	readUntil(t, x, func() bool { return x.tab.lookup(0x500009) != 0 })
	n := settle(t, x, p)

	focusIn := func(win uint32, mode byte) {
		p.event(9, func(e *[32]byte) {
			binary.LittleEndian.PutUint32(e[4:], win)
			e[8] = mode
		})
	}
	x.feed(t, p, func() { focusIn(0x500009, 0) })
	x.feed(t, p, func() { focusIn(0x500002, byte(xproto.NotifyModeGrab)) })
	x.feed(t, p, func() { focusIn(0x500002, byte(xproto.NotifyModeUngrab)) })
	require.False(t, x.refocus)
	settle(t, x, p)
	require.Empty(t, since(p, n, opSetInputFocus))

	x.feed(t, p, func() { focusIn(0x500002, 0) })
	settle(t, x, p)
	require.Len(t, since(p, n, opSetInputFocus), 1, "a normal FocusIn on another managed window is still denied")
}

func TestWMChangeStateIgnored(t *testing.T) {
	x, p, h, _, _ := attached(t)
	newWin(t, x, p, h, 0x500001)
	n := settle(t, x, p)
	x.feed(t, p, func() { p.clientMessage(0x500001, p.atom("WM_CHANGE_STATE"), 3) })
	settle(t, x, p)
	require.Empty(t, since(p, n, opConfigureWindow)) // the mocked host sees no call either
}

func TestUnmanagedGeometryAllocations(t *testing.T) {
	x, p, _, _, _ := attached(t)
	x.SetLayout(layoutOf(out(0, 0, 1920, 1080)))
	s := x.insert(0x500009)
	r := x.tab.rec(s)
	r.kind, r.mapped, r.ready, r.ovPaired = kindUnmanaged, true, true, true
	r.x, r.y, r.w, r.h = 30, 40, 100, 50
	r.ov = x.layoutRect(r)
	sinkPeer(t, x, p)
	a := rootConfigureNotify(0x500009, 31, 41, 100, 50)
	b := rootConfigureNotify(0x500009, 30, 40, 100, 50) // back to the reported rect
	allocs := testing.AllocsPerRun(1000, func() {
		x.onEvent(&a)
		x.onEvent(&b)
		x.flush()
	})
	require.Zero(t, allocs)
	require.True(t, r.ovPaired)
}

func TestFocusInUnmanagedAllocations(t *testing.T) {
	x, p, _, _, _ := attached(t)
	s := x.insert(0x500009)
	x.tab.rec(s).kind = kindUnmanaged
	sinkPeer(t, x, p)
	ev := xturbo.Event{Kind: xproto.KindFocusIn}
	binary.LittleEndian.PutUint32(ev.Raw[4:], 0x500009)
	grab := ev
	grab.Raw[8] = byte(xproto.NotifyModeGrab)
	allocs := testing.AllocsPerRun(1000, func() {
		x.onEvent(&ev)
		x.onEvent(&grab)
		x.flush()
	})
	require.Zero(t, allocs)
	require.False(t, x.refocus)
}

func TestDropWindowClearsQueuedOverride(t *testing.T) {
	x, _, _, _, _ := attached(t)
	s := x.insert(0x500009)
	r := x.tab.rec(s)
	r.kind, r.ovQueued = kindUnmanaged, true
	x.dropWindow(s)
	require.False(t, r.ovQueued)
}
