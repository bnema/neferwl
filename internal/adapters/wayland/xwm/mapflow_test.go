package xwm

import (
	"context"
	"encoding/binary"
	"testing"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// harness bundles an attached XWM, its peer and the host mock.
type harness struct {
	t *testing.T
	x *XWM
	p *xpeer
	h *mockhost

	ready map[uint32]WindowInfo // by xid, filled by Ready
	next  ports.WindowID
}

func newHarness(t *testing.T, mutate func(p *xpeer)) *harness {
	t.Helper()
	x, p := bootPeer(t, mutate)
	hs := &harness{t: t, x: x, p: p, h: newMockhost(t), ready: map[uint32]WindowInfo{}}
	src := newMockfdSource(t)
	src.EXPECT().Remove().Maybe()
	src.EXPECT().SetWritable(mock.Anything).Return(nil).Maybe()
	post, _ := newPost()
	x.attach(src, hs.h, post)
	return hs
}

func (hs *harness) expectCreated(win uint32) {
	hs.next++
	hs.h.EXPECT().Created(mock.Anything, win).Return(hs.next).Once()
}

func (hs *harness) expectReady(win uint32) {
	hs.h.EXPECT().Ready(mock.Anything, mock.Anything).Run(func(_ Slot, info WindowInfo) {
		hs.ready[win] = info
	}).Once()
}

func (hs *harness) create(win uint32) Slot {
	hs.t.Helper()
	hs.expectCreated(win)
	hs.p.createNotify(win, false)
	readUntil(hs.t, hs.x, func() bool { return hs.x.Slot(win) != 0 })
	return hs.x.Slot(win)
}

// mapAndWait creates and maps win and waits for Ready.
func (hs *harness) mapAndWait(win uint32) Slot {
	hs.t.Helper()
	s := hs.create(win)
	hs.expectReady(win)
	hs.p.mapRequest(win)
	readUntil(hs.t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	return s
}

func (hs *harness) str(win uint32, atom string, typ uint32, s string) {
	hs.p.setProp(win, hs.p.atom(atom), typ, 8, []byte(s))
}

func (hs *harness) words(win uint32, atom string, typ uint32, w ...uint32) {
	hs.p.setProp(win, hs.p.atom(atom), typ, 32, le32(w...))
}

func (hs *harness) atoms(win uint32, atom string, names ...string) {
	var w []uint32
	for _, n := range names {
		w = append(w, hs.p.atom(n))
	}
	hs.words(win, atom, uint32(xproto.AtomAtom), w...)
}

func (hs *harness) classic(win uint32) {
	hs.p.setProp(win, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("inst\x00Class\x00"))
	hs.str(win, "_NET_WM_NAME", hs.p.atom("UTF8_STRING"), "net title")
	hs.p.setProp(win, uint32(xproto.AtomWmName), uint32(xproto.AtomString), 8, []byte("old title"))
	hs.words(win, "_NET_WM_PID", uint32(xproto.AtomCardinal), 4242)
	hs.p.set(func(p *xpeer) { p.geoms[win] = xpeerGeom{w: 300, h: 200} })
}

func (hs *harness) count(match func(r xpeerReq) bool) int {
	n := 0
	for _, r := range hs.p.requests() {
		if match(r) {
			n++
		}
	}
	return n
}

func (hs *harness) waitMap(win uint32) {
	hs.t.Helper()
	readUntil(hs.t, hs.x, func() bool {
		return hs.count(func(r xpeerReq) bool { return r.major == opMapWindow && r.u32(4) == win }) > 0
	})
}

func TestMapFlowBatchesRequestsAndReadyFiresOnce(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.create(win)
	base := len(hs.p.requests())

	hs.p.holdReplies()
	hs.p.mapRequest(win)
	hs.expectReady(win)
	hs.waitMap(win)

	reqs := hs.p.requests()[base:]
	want := []uint32{
		uint32(xproto.AtomWmClass), uint32(xproto.AtomWmName), hs.p.atom("_NET_WM_NAME"), uint32(xproto.AtomWmHints),
		uint32(xproto.AtomWmNormalHints), hs.p.atom("WM_PROTOCOLS"), uint32(xproto.AtomWmTransientFor),
		hs.p.atom("_NET_WM_WINDOW_TYPE"), hs.p.atom("_NET_WM_STATE"), hs.p.atom("_NET_WM_PID"),
	}
	require.GreaterOrEqual(t, len(reqs), len(want)+2)
	for i, a := range want {
		require.EqualValues(t, opGetProperty, reqs[i].major, i)
		require.Equal(t, uint32(win), reqs[i].u32(4))
		require.Equal(t, a, reqs[i].u32(8), i)
		require.EqualValues(t, 1024, reqs[i].u32(20), "long length: one 4 KiB read")
	}
	require.EqualValues(t, opGetGeometry, reqs[len(want)].major)
	require.EqualValues(t, opMapWindow, reqs[len(want)+1].major, "MapWindow in the same batch")

	hs.p.release() // every reply arrives after MapWindow went out
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	info := hs.ready[win]
	require.Equal(t, WindowInfo{PID: 4242, Title: "net title", AppID: "Class"}, info, "_NET_WM_NAME wins, class is the second string")

	state, ok := hs.p.prop(win, "WM_STATE")
	require.True(t, ok)
	require.Equal(t, []uint32{1, 0}, atomsOf(state))
	list, ok := hs.p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.True(t, ok)
	require.Equal(t, []uint32{win}, atomsOf(list))
}

func TestMapFlowFloatingAndModal(t *testing.T) {
	hint := func(minw, minh, maxw, maxh uint32) []byte {
		return le32(hintMinSize|hintMaxSize, 0, 0, 0, 0, minw, minh, maxw, maxh, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	cases := []struct {
		name  string
		setup func(hs *harness, win uint32)
		want  func(info WindowInfo)
	}{
		{"dialog type", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_DIALOG")
		}, func(i WindowInfo) {
			require.True(t, i.Floating)
			require.Equal(t, 300, i.Width)
			require.Equal(t, 200, i.Height)
		}},
		{"utility type", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_UTILITY")
		}, func(i WindowInfo) { require.True(t, i.Floating) }},
		{"splash type", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_SPLASH")
		}, func(i WindowInfo) { require.True(t, i.Floating) }},
		{"normal type", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL")
		}, func(i WindowInfo) { require.False(t, i.Floating); require.Zero(t, i.Width) }},
		{"fixed size", func(hs *harness, w uint32) {
			hs.p.setProp(w, uint32(xproto.AtomWmNormalHints), uint32(xproto.AtomWmSizeHints), 32, hint(200, 100, 200, 100))
		}, func(i WindowInfo) { require.True(t, i.Floating) }},
		{"resizable", func(hs *harness, w uint32) {
			hs.p.setProp(w, uint32(xproto.AtomWmNormalHints), uint32(xproto.AtomWmSizeHints), 32, hint(100, 100, 800, 600))
		}, func(i WindowInfo) { require.False(t, i.Floating) }},
		{"modal", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_STATE", "_NET_WM_STATE_MODAL")
		}, func(i WindowInfo) { require.True(t, i.Modal); require.False(t, i.Fullscreen) }},
		{"fullscreen at map", func(hs *harness, w uint32) {
			hs.atoms(w, "_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN")
		}, func(i WindowInfo) { require.True(t, i.Fullscreen) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hs := newHarness(t, nil)
			const win = 0x500001
			hs.classic(win)
			c.setup(hs, win)
			hs.mapAndWait(win)
			c.want(hs.ready[win])
		})
	}
}

func TestMapFlowTransientForManagedWindowFloats(t *testing.T) {
	hs := newHarness(t, nil)
	const parent, child = 0x500001, 0x500002
	hs.classic(parent)
	ps := hs.mapAndWait(parent)
	hs.classic(child)
	hs.p.setProp(child, uint32(xproto.AtomWmTransientFor), uint32(xproto.AtomWindow), 32, le32(parent))
	hs.mapAndWait(child)
	require.True(t, hs.ready[child].Floating)
	require.Equal(t, ps, hs.ready[child].Parent)
}

func TestMapFlowTransientForUnknownWindowIsNotAParent(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.setProp(win, uint32(xproto.AtomWmTransientFor), uint32(xproto.AtomWindow), 32, le32(0x777777))
	hs.mapAndWait(win)
	require.Zero(t, hs.ready[win].Parent)
	require.False(t, hs.ready[win].Floating)
}

func TestMapFlowClassFallsBackToInstance(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.setProp(win, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("onlyinstance\x00"))
	hs.mapAndWait(win)
	require.Equal(t, "onlyinstance", hs.ready[win].AppID)
}

func TestMapFlowTitleFallsBackToWMName(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_NAME"]) })
	hs.mapAndWait(win)
	require.Equal(t, "old title", hs.ready[win].Title)
}

func TestMapFlowPIDFromXRes(t *testing.T) {
	hs := newHarness(t, func(p *xpeer) { p.pids = map[uint32]uint32{0x500001: 777} })
	const win = 0x500001
	hs.classic(win)
	hs.p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_PID"]) })
	hs.create(win)
	hs.expectReady(win)
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	require.Equal(t, 777, hs.ready[win].PID, "Ready waited for the QueryClientIds reply")
	require.Equal(t, 1, hs.count(func(r xpeerReq) bool { return r.major == 144 && r.minor == 4 }))
	require.Zero(t, hs.x.tab.rec(hs.x.Slot(win)).pending)
}

func TestMapFlowPIDUnknownStillReady(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_PID"]) })
	hs.mapAndWait(win)
	require.Zero(t, hs.ready[win].PID)
}

func TestMapFlowNoXResAsksNothing(t *testing.T) {
	exts := fullPeerExts()
	delete(exts, "X-Resource")
	fd, p := startXPeer(t, exts)
	x, err := Boot(context.Background(), fd, testLog())
	require.NoError(t, err)
	t.Cleanup(x.CloseConn)
	h := newMockhost(t)
	src := newMockfdSource(t)
	src.EXPECT().Remove().Maybe()
	post, _ := newPost()
	x.attach(src, h, post)
	hs := &harness{t: t, x: x, p: p, h: h, ready: map[uint32]WindowInfo{}}
	const win = 0x500001
	hs.classic(win)
	p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_PID"]) })
	hs.mapAndWait(win)
	require.Zero(t, hs.count(func(r xpeerReq) bool { return r.major == 144 }))
}

func TestMapFlowBadWindowOnPropertiesNeverReady(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.set(func(p *xpeer) { p.bad[win] = true })
	s := hs.create(win)
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Errors >= uint64(propCount)+1 })
	require.Zero(t, hs.x.tab.rec(s).pending, "failed replies are no longer awaited")
	require.False(t, hs.x.tab.rec(s).ready)
	require.Zero(t, hs.x.Stats().Unattributed)

	hs.h.EXPECT().Dissociate(s, true).Once()
	hs.p.destroyNotify(win)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(win) == 0 })
}

func TestMapRequestTwiceBeforeReadyAsksOnce(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.create(win)
	hs.p.holdReplies()
	hs.p.mapRequest(win)
	hs.p.mapRequest(win)
	hs.expectReady(win)
	hs.waitMap(win)
	hs.p.release()
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	require.Equal(t, 1, hs.count(func(r xpeerReq) bool { return r.major == opMapWindow && r.u32(4) == win }))
}

func TestMapNotifyMarksMapped(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.p.mapNotify(win)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).mapped })
}

// Property changes after Ready.

func (hs *harness) notify(win uint32, atom string, deleted bool) {
	hs.t.Helper()
	before := hs.x.Stats().Replies
	hs.p.propertyNotify(win, hs.p.atom(atom), deleted)
	if deleted {
		hs.p.propertyNotify(win, hs.p.atom("WM_PROTOCOLS"), false) // sentinel: one reply follows
		readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Replies > before })
		return
	}
	readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Replies > before })
}

func TestPropertyChangedTitleReachesHostOnce(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)

	hs.h.EXPECT().Title(s, "renamed").Once()
	hs.str(win, "_NET_WM_NAME", hs.p.atom("UTF8_STRING"), "renamed")
	hs.notify(win, "_NET_WM_NAME", false)
	require.Equal(t, "renamed", hs.x.tab.rec(s).title)

	// Same value again: no host call (the mock fails on an unexpected one).
	hs.notify(win, "_NET_WM_NAME", false)

	// WM_NAME is ignored while _NET_WM_NAME is set.
	hs.p.setProp(win, uint32(xproto.AtomWmName), uint32(xproto.AtomString), 8, []byte("legacy"))
	before := hs.x.Stats().Events
	hs.p.propertyNotify(win, uint32(xproto.AtomWmName), false)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events > before })
	require.Equal(t, "renamed", hs.x.tab.rec(s).title)
}

func TestPropertyNetNameDeletedFallsBackToWMName(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_NAME"]) })
	hs.h.EXPECT().Title(s, "old title").Once()
	hs.p.propertyNotify(win, hs.p.atom("_NET_WM_NAME"), true)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).title == "old title" })
}

func TestPropertyClassChangeReachesHost(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.h.EXPECT().AppID(s, "Other").Once()
	hs.p.setProp(win, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("i\x00Other\x00"))
	hs.p.propertyNotify(win, uint32(xproto.AtomWmClass), false)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).class == "Other" })
}

func TestPropertyTransientChangeReachesHost(t *testing.T) {
	hs := newHarness(t, nil)
	const a, b = 0x500001, 0x500002
	hs.classic(a)
	as := hs.mapAndWait(a)
	hs.classic(b)
	bs := hs.mapAndWait(b)
	hs.h.EXPECT().Parent(bs, as).Once()
	hs.p.setProp(b, uint32(xproto.AtomWmTransientFor), uint32(xproto.AtomWindow), 32, le32(a))
	hs.p.propertyNotify(b, uint32(xproto.AtomWmTransientFor), false)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(bs).transient == a })
}

func TestPropertyInputProtocolsAndHints(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.atoms(win, "WM_PROTOCOLS", "WM_DELETE_WINDOW", "WM_TAKE_FOCUS")
	hs.p.setProp(win, uint32(xproto.AtomWmHints), uint32(xproto.AtomWmHints), 32, le32(hintInput, 0, 0, 0, 0, 0, 0, 0, 0))
	s := hs.mapAndWait(win)
	r := hs.x.tab.rec(s)
	require.True(t, r.deleteWindow)
	require.True(t, r.takeFocus)
	require.False(t, r.input, "WM_HINTS input = false")

	// No WM_HINTS: input defaults to true.
	const other = 0x500002
	hs.classic(other)
	o := hs.mapAndWait(other)
	require.True(t, hs.x.tab.rec(o).input)
	require.False(t, hs.x.tab.rec(o).deleteWindow)
}

func TestPropertyUnwatchedAtomAsksNothing(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.mapAndWait(win)
	n := len(hs.p.requests())
	hs.p.propertyNotify(win, hs.p.atom("_MOTIF_WM_HINTS"), false)
	hs.p.propertyNotify(0x999999, hs.p.atom("_NET_WM_NAME"), false) // unknown window
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events >= 2 })
	require.Len(t, hs.p.requests(), n)
}

// Pairing.

func (hs *harness) serialMsg(win uint32, serial uint64) {
	hs.p.clientMessage(win, hs.p.atom("WL_SURFACE_SERIAL"), uint32(serial), uint32(serial>>32))
	hs.t.Helper()
	before := hs.x.Stats().Events
	readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Events > before })
}

func TestPairingSerialBeforeSetSerial(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.serialMsg(win, 0x1_0000_0002)
	require.EqualValues(t, 0x1_0000_0002, hs.x.tab.rec(s).serial, "high word in data32[1]")
	hs.h.EXPECT().Associate(s, uint64(99)).Once()
	hs.x.SetSerial(99, 0x1_0000_0002)
	require.EqualValues(t, 99, hs.x.tab.rec(s).surface)
	require.Zero(t, hs.x.unpairedN)
}

func TestPairingSetSerialBeforeSerial(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.x.SetSerial(99, 7)
	require.Equal(t, 1, hs.x.unpairedN)
	hs.h.EXPECT().Associate(s, uint64(99)).Once()
	hs.serialMsg(win, 7)
	require.Zero(t, hs.x.unpairedN)
}

func TestPairingWaitsForReady(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.create(win)
	hs.x.SetSerial(5, 3)
	hs.serialMsg(win, 3) // not mapped yet: no Associate (the mock fails on one)
	require.Zero(t, hs.x.tab.rec(s).surface)

	hs.h.EXPECT().Associate(s, uint64(5)).Once()
	hs.expectReady(win)
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).surface != 0 })
}

func TestPairingSerialForUnknownWindowIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	hs.x.SetSerial(5, 3)
	hs.serialMsg(0x888888, 3)
	require.Equal(t, 1, hs.x.unpairedN, "stays unpaired")
}

func TestPairingSurfaceGoneBeforePair(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.x.SetSerial(5, 3)
	hs.x.SurfaceGone(5)
	require.Zero(t, hs.x.unpairedN)
	hs.serialMsg(win, 3) // the surface is gone: nothing to pair with
	require.Zero(t, hs.x.tab.rec(s).surface)
}

func TestPairingSurfaceGoneAfterPairClearsLink(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.serialMsg(win, 3)
	hs.h.EXPECT().Associate(s, uint64(5)).Once()
	hs.x.SetSerial(5, 3)
	hs.x.SurfaceGone(5)
	require.Zero(t, hs.x.tab.rec(s).surface)
}

func TestPairingDuplicateSerialIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.serialMsg(win, 3)
	hs.serialMsg(win, 4)
	require.EqualValues(t, 3, hs.x.tab.rec(s).serial)
	hs.x.SetSerial(8, 4) // nobody announces 4: stays unpaired
	require.Equal(t, 1, hs.x.unpairedN)
}

func TestPairingUnmanagedKeepsSurfaceWithoutAssociate(t *testing.T) {
	hs := newHarness(t, nil)
	hs.p.createNotify(0x500009, true)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(0x500009) != 0 })
	hs.serialMsg(0x500009, 3)
	hs.x.SetSerial(5, 3) // the mock fails on an Associate
	require.EqualValues(t, 5, hs.x.tab.rec(hs.x.Slot(0x500009)).surface)
}

func TestPairingTableOverflowDropsOldest(t *testing.T) {
	hs := newHarness(t, nil)
	for i := uint64(1); i <= unpairedCap+1; i++ {
		hs.x.SetSerial(100+i, i)
	}
	require.Equal(t, unpairedCap, hs.x.unpairedN)
	for i := 0; i < hs.x.unpairedN; i++ {
		require.NotEqual(t, uint64(1), hs.x.unpaired[i].serial, "oldest dropped")
	}
}

func TestPairingZeroSerialIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	hs.x.SetSerial(5, 0)
	require.Zero(t, hs.x.unpairedN)
}

func TestLegacySurfaceIDCountedAndIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.p.clientMessage(win, hs.p.atom("WL_SURFACE_ID"), 12)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().LegacyID == 1 })
	require.Zero(t, hs.x.tab.rec(s).serial)
}

func TestNetWMStateClientMessageForwardsFullscreen(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.h.EXPECT().Fullscreen(s, true).Once()
	hs.p.clientMessage(win, hs.p.atom("_NET_WM_STATE"), 1, hs.p.atom("_NET_WM_STATE_FULLSCREEN"))
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).fullscreen })
	hs.h.EXPECT().Fullscreen(s, false).Once() // toggle
	hs.p.clientMessage(win, hs.p.atom("_NET_WM_STATE"), 2, hs.p.atom("_NET_WM_STATE_FULLSCREEN"))
	readUntil(t, hs.x, func() bool { return !hs.x.tab.rec(s).fullscreen })
	// Not written back by the message itself.
	st, ok := hs.p.prop(win, "_NET_WM_STATE")
	require.True(t, ok)
	require.Empty(t, st.data)
}

// Unmap, destroy, remap.

func TestUnmapDissociatesAndRemapRereadsProperties(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)

	// A synthetic UnmapNotify (ICCCM withdraw request) is not an unmap.
	hs.p.unmapNotify(win, true)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events >= 2 })
	require.True(t, hs.x.tab.rec(s).ready)

	hs.h.EXPECT().Dissociate(s, false).Once()
	hs.p.unmapNotify(win, false)
	readUntil(t, hs.x, func() bool { return !hs.x.tab.rec(s).ready })
	r := hs.x.tab.rec(s)
	require.False(t, r.mapping)
	require.Zero(t, r.serial)
	hs.p.waitReq(0, func(q xpeerReq) bool {
		return q.major == opSetInputFocus || q.major == opChangeProperty && q.u32(4) == win
	})
	readUntil(t, hs.x, func() bool {
		v, ok := hs.p.prop(win, "WM_STATE")
		return ok && len(v.data) == 8 && binary.LittleEndian.Uint32(v.data) == 0
	})
	_, ok := hs.p.prop(win, "_NET_WM_STATE")
	require.False(t, ok, "_NET_WM_STATE deleted")
	list, _ := hs.p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Empty(t, list.data)

	// Remap starts over.
	delete(hs.ready, win)
	hs.expectReady(win)
	n := hs.count(func(q xpeerReq) bool { return q.major == opGetProperty && q.u32(4) == win })
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	require.Equal(t, n+propCount, hs.count(func(q xpeerReq) bool { return q.major == opGetProperty && q.u32(4) == win }))
	list, _ = hs.p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Equal(t, []uint32{win}, atomsOf(list))
}

func TestDestroyWhileMappingNeverReady(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.create(win)
	hs.p.holdReplies()
	hs.p.mapRequest(win)
	hs.waitMap(win)
	hs.h.EXPECT().Dissociate(s, true).Once()
	hs.p.destroyNotify(win)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(win) == 0 })
	replies := hs.x.Stats().Replies
	hs.p.release() // replies for a window that is gone: xturbo retired their slots
	readUntil(t, hs.x, func() bool { return hs.x.conn.Stats().DroppedReplies >= uint64(propCount) })
	require.Equal(t, replies, hs.x.Stats().Replies, "none reaches the XWM")
}

func TestStaleRepliesAfterSlotReuseAreIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	const old, fresh = 0x500001, 0x500002
	hs.classic(old)
	hs.classic(fresh)
	s := hs.create(old)
	hs.p.holdReplies()
	hs.p.mapRequest(old)
	hs.waitMap(old)
	hs.h.EXPECT().Dissociate(s, true).Once()
	hs.p.destroyNotify(old)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(old) == 0 })

	hs.expectCreated(fresh)
	hs.p.createNotify(fresh, false)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(fresh) != 0 })
	require.Equal(t, s, hs.x.Slot(fresh), "the freed slot is reused")

	hs.p.release()
	readUntil(t, hs.x, func() bool { return hs.x.conn.Stats().DroppedReplies >= uint64(propCount) })

	// A reply that does get through with the old tag (slot reused, generation
	// bumped) is ignored.
	oldTag := s | Slot(hs.x.tab.rec(s).gen-1)<<16
	rep := make([]byte, 32)
	rep[0] = 1
	hs.x.onReply(&xturbo.Reply{Kind: kindProp, Args: [4]uint32{uint32(oldTag), propClass}, Raw: rep})
	hs.x.onReply(&xturbo.Reply{Kind: kindGeom, Args: [4]uint32{uint32(oldTag)}, Raw: rep})
	require.EqualValues(t, 2, hs.x.Stats().Stale)
	require.False(t, hs.x.tab.rec(s).ready, "stale replies must not complete the new window")
	require.Zero(t, hs.x.tab.rec(s).pending)
}

// Reparenting.

func TestReparentAwayFromRootRemovesManagedWindow(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.h.EXPECT().Dissociate(s, true).Once()
	hs.p.reparentNotify(win, 0x600000)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(win) == 0 })
	list, _ := hs.p.prop(peerRoot, "_NET_CLIENT_LIST")
	require.Empty(t, list.data)
	require.Zero(t, hs.x.tab.n)
}

func TestReparentAwayOfUnmanagedWindowRemovesIt(t *testing.T) {
	hs := newHarness(t, nil)
	hs.p.createNotify(0x500009, true)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(0x500009) != 0 })
	hs.p.reparentNotify(0x500009, 0x600000)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(0x500009) == 0 })
}

func TestReparentIntoRootAdoptsWindow(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.expectCreated(win)
	hs.p.reparentNotify(win, peerRoot)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(win) != 0 })
	require.Equal(t, kindManaged, hs.x.tab.rec(hs.x.Slot(win)).kind)

	// Already known: no second Created.
	hs.p.reparentNotify(win, peerRoot)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events >= 2 })
}

func TestReparentIntoRootOfOverrideRedirectWindowStaysUnmanaged(t *testing.T) {
	hs := newHarness(t, nil)
	hs.p.event(21, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], 0x500009)
		binary.LittleEndian.PutUint32(b[12:], peerRoot)
		b[20] = 1
	})
	readUntil(t, hs.x, func() bool { return hs.x.Slot(0x500009) != 0 })
	require.Equal(t, kindUnmanaged, hs.x.tab.rec(hs.x.Slot(0x500009)).kind)
}

func TestReparentOfWMWindowIgnored(t *testing.T) {
	hs := newHarness(t, nil)
	hs.p.reparentNotify(hs.x.wmWin, peerRoot)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events >= 1 })
	require.Zero(t, hs.x.Slot(hs.x.wmWin))
}

// Window known only before Attach: Attach replays the map flow.
func TestAttachMapsWindowsRequestedBeforeAttach(t *testing.T) {
	x, p := bootPeer(t, nil)
	const win = 0x500001
	p.setProp(win, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("a\x00B\x00"))
	p.createNotify(win, false)
	p.mapRequest(win)
	readUntil(t, x, func() bool { return x.Stats().Events >= 2 })
	h := newMockhost(t)
	src := newMockfdSource(t)
	src.EXPECT().Remove().Maybe()
	post, _ := newPost()
	var got WindowInfo
	done := false
	h.EXPECT().Created(mock.Anything, uint32(win)).Return(ports.WindowID(1)).Once()
	h.EXPECT().Ready(mock.Anything, mock.Anything).Run(func(_ Slot, i WindowInfo) { got, done = i, true }).Once()
	x.attach(src, h, post)
	readUntil(t, x, func() bool { return done })
	require.Equal(t, "B", got.AppID)
}

// Replies complete a map in any order (a server may answer the geometry
// before the properties it was asked for first): Ready fires on the last one.
func TestMapFlowReadyFiresOnLastReplyInAnyOrder(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.create(win)
	r := hs.x.tab.rec(s)
	r.mapping, r.pending = true, propMaskAll&^propClientIDs
	hs.expectReady(win)
	order := []int{propCount, propPID, propClass, propNetName, propName, propHints, propNormalHints, propProtocols, propTransientFor, propWindowType, propState}
	for i, bit := range order {
		require.False(t, hs.x.tab.rec(s).ready, i)
		if bit == propCount { // the geometry
			hs.x.tab.rec(s).w, hs.x.tab.rec(s).h = 300, 200
			hs.x.tab.rec(s).pending &^= propGeom
			hs.x.checkReady(s)
			continue
		}
		var val []byte
		var format uint8 = 8
		switch bit {
		case propClass:
			val = []byte("a\x00B\x00")
		case propNetName:
			val = []byte("t")
		case propPID:
			val, format = le32(5), 32
		default:
			format = 32
		}
		x := hs.x
		x.applyProp(s, bit, format, val)
		x.tab.rec(s).pending &^= 1 << bit
		x.checkReady(s)
	}
	require.True(t, hs.x.tab.rec(s).ready)
	require.Equal(t, "B", hs.ready[win].AppID)
	require.Equal(t, "t", hs.ready[win].Title)
	require.Equal(t, 5, hs.ready[win].PID)
}
