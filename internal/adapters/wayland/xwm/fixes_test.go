package xwm

import (
	"encoding/binary"
	"testing"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func getProps(p *xpeer, skip int, win uint32) int {
	n := 0
	for _, r := range since(p, skip, opGetProperty) {
		if r.u32(4) == win {
			n++
		}
	}
	return n
}

// Our own _NET_WM_STATE writes must not be read back once the window is ready.
func TestOwnNetWMStateWriteIsNotReadBack(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)

	hs.x.Configure(s, ports.ConfigureWindow{Width: 300, Height: 200, Fullscreen: true})
	n := settle(t, hs.x, hs.p)
	require.True(t, hs.x.tab.rec(s).fullscreen, "the written state is the known state")
	st, _ := hs.p.prop(win, "_NET_WM_STATE")
	require.Equal(t, []uint32{hs.p.atom("_NET_WM_STATE_FULLSCREEN")}, atomsOf(st))

	ev := hs.x.Stats().Events
	hs.p.propertyNotify(win, hs.p.atom("_NET_WM_STATE"), false)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Events > ev })
	settle(t, hs.x, hs.p)
	require.Zero(t, getProps(hs.p, n, win), "no GetProperty")

}

// A pre-map XMoveResize is honoured, floating or not.
func TestConfigureRequestBeforeReadyIsHonoured(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.create(win)
	n := settle(t, hs.x, hs.p)

	mask := uint16(xproto.ConfigWindowX | xproto.ConfigWindowY | xproto.ConfigWindowWidth | xproto.ConfigWindowHeight)
	hs.x.feed(t, hs.p, func() { sendConfigureRequest(hs.p, win, mask, 11, 22, 640, 70000&0xffff) })
	settle(t, hs.x, hs.p)
	cw := since(hs.p, n, opConfigureWindow)
	require.Len(t, cw, 1)
	require.Equal(t, []uint32{11, 22, 640, 4464}, []uint32{cw[0].u32(12), cw[0].u32(16), cw[0].u32(20), cw[0].u32(24)})
	require.Len(t, sentEvents(hs.p, n, win), 1)

	hs.expectReady(win)
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })
	require.Equal(t, 640, hs.ready[win].Width)
	require.Equal(t, 4464, hs.ready[win].Height)
	require.False(t, hs.ready[win].Floating)
}

// After a remap the state is rewritten from scratch, FOCUSED included.
func TestRemapRewritesStateWithFocused(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.mapAndWait(win)
	hs.x.Focus(s)
	settle(t, hs.x, hs.p)
	st, _ := hs.p.prop(win, "_NET_WM_STATE")
	require.Equal(t, []uint32{hs.p.atom("_NET_WM_STATE_FOCUSED")}, atomsOf(st))

	hs.h.EXPECT().Dissociate(s, false).Once()
	hs.p.unmapNotify(win, false)
	readUntil(t, hs.x, func() bool { return !hs.x.tab.rec(s).mapping })
	delete(hs.ready, win)
	hs.expectReady(win)
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { _, ok := hs.ready[win]; return ok })

	hs.x.Focus(s)
	settle(t, hs.x, hs.p)
	st, _ = hs.p.prop(win, "_NET_WM_STATE")
	require.Equal(t, []uint32{hs.p.atom("_NET_WM_STATE_FOCUSED")}, atomsOf(st))
}

// Replies of a previous map never satisfy the next one (xturbo usually retires
// their slots first; the generation guards the rest).
func TestStaleRepliesOfPreviousMapAreDropped(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.create(win)
	hs.p.holdReplies() // the second map is never answered
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).mapping })
	oldTag := hs.x.tab.tag(s)
	hs.h.EXPECT().Dissociate(s, false).Once()
	hs.p.unmapNotify(win, false)
	readUntil(t, hs.x, func() bool { return !hs.x.tab.rec(s).mapping })
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { return hs.x.tab.rec(s).mapping })
	require.NotEqual(t, oldTag, hs.x.tab.tag(s))

	rep := make([]byte, 32)
	rep[0] = 1
	for idx := 0; idx < propCount; idx++ {
		hs.x.onReply(&xturbo.Reply{Kind: kindProp, Args: [4]uint32{oldTag, uint32(idx)}, Raw: rep})
	}
	hs.x.onReply(&xturbo.Reply{Kind: kindGeom, Args: [4]uint32{oldTag}, Raw: rep})
	require.EqualValues(t, propCount+1, hs.x.Stats().Stale)
	require.False(t, hs.x.tab.rec(s).ready)
	require.Equal(t, propMaskAll&^propClientIDs, hs.x.tab.rec(s).pending, "the new map still awaits its own replies")
}

func TestGetGeometryBadDrawableIsNotReady(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	s := hs.create(win)
	hs.p.set(func(p *xpeer) { p.bad[win] = true })
	hs.p.mapRequest(win)
	readUntil(t, hs.x, func() bool { return hs.x.Stats().Errors >= uint64(propCount)+1 })
	require.Zero(t, hs.x.Stats().Unattributed)
	require.False(t, hs.x.tab.rec(s).ready)
	hs.h.EXPECT().Dissociate(s, true).Once()
	hs.p.destroyNotify(win)
	readUntil(t, hs.x, func() bool { return hs.x.Slot(win) == 0 })
}

// A host that refocuses the window while dissociating it must not leave a
// dangling active slot.
func TestForgetWindowClearsActiveSetByHost(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	h.EXPECT().Dissociate(s, true).Run(func(sl Slot, _ bool) { x.Focus(sl) }).Once()
	p.destroyNotify(win)
	readUntil(t, x, func() bool { return x.Slot(win) == 0 })
	require.Zero(t, x.active)
	require.False(t, x.refocus)
	settle(t, x, p)
	act, _ := p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
	require.Equal(t, []uint32{0}, atomsOf(act))
}

func TestTitleAndAppIDAreValidUTF8(t *testing.T) {
	hs := newHarness(t, nil)
	const win = 0x500001
	hs.classic(win)
	hs.p.set(func(p *xpeer) { delete(p.props[win], p.atoms["_NET_WM_NAME"]) })
	hs.p.setProp(win, uint32(xproto.AtomWmName), uint32(xproto.AtomString), 8, []byte("caf\xe9"))
	hs.p.setProp(win, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("i\x00C\xe9\x00"))
	s := hs.mapAndWait(win)
	require.Equal(t, "caf\uFFFD", hs.ready[win].Title)
	require.Equal(t, "C\uFFFD", hs.ready[win].AppID)

	// Unchanged after sanitising: no host call.
	hs.notifyWMName(win)
	// A changed truncated multibyte title reaches the host sanitised.
	hs.h.EXPECT().Title(s, "\u00e9\uFFFD").Once()
	hs.p.setProp(win, uint32(xproto.AtomWmName), uint32(xproto.AtomString), 8, []byte("\xc3\xa9\xe2\x82"))
	hs.notifyWMName(win)
}

func TestSyntheticConfigureNotifyClampsCoordinates(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const win = 0x500001
	s := newWin(t, x, p, h, win)
	n := settle(t, x, p)
	r := x.tab.rec(s)
	r.x, r.y, r.w, r.h = 70000, -70000, 70000, 5
	x.sendConfigureNotify(r)
	settle(t, x, p)
	ev := sentEvents(p, n, win)
	require.Len(t, ev, 1)
	require.EqualValues(t, 32767, int16(binary.LittleEndian.Uint16(ev[0][16:])))
	require.EqualValues(t, -32768, int16(binary.LittleEndian.Uint16(ev[0][18:])))
	require.EqualValues(t, 65535, binary.LittleEndian.Uint16(ev[0][20:]))
	require.EqualValues(t, 5, binary.LittleEndian.Uint16(ev[0][22:]))
}

// A window withdrawn before Attach is not mapped by Attach; a pre-attach
// ConfigureRequest is honoured.
func TestUnmapAndConfigureBeforeAttach(t *testing.T) {
	x, p := bootPeer(t, nil)
	const gone, sized = 0x500001, 0x500002
	for _, w := range []uint32{gone, sized} {
		p.setProp(w, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), 8, []byte("a\x00B\x00"))
		p.geoms[w] = xpeerGeom{w: 300, h: 200}
		p.createNotify(w, false)
		p.mapRequest(w)
	}
	p.unmapNotify(gone, false)
	mask := uint16(xproto.ConfigWindowWidth | xproto.ConfigWindowHeight)
	sendConfigureRequest(p, sized, mask, 0, 0, 500, 400)
	readUntil(t, x, func() bool { return x.Stats().Events >= 6 })
	n := len(p.requests())

	h := newMockhost(t)
	src := newMockfdSource(t)
	src.EXPECT().Remove().Maybe()
	post, _ := newPost()
	var got WindowInfo
	done := false
	h.EXPECT().Created(mock.Anything, mock.Anything).Return(ports.WindowID(1)).Twice()
	h.EXPECT().Ready(mock.Anything, mock.Anything).Run(func(_ Slot, i WindowInfo) { got, done = i, true }).Once()
	x.attach(src, h, post)
	readUntil(t, x, func() bool { return done })
	settle(t, x, p)
	for _, r := range since(p, n, opMapWindow) {
		require.NotEqual(t, uint32(gone), r.u32(4), "withdrawn window must not be mapped")
	}
	require.Equal(t, 500, got.Width)
	require.Equal(t, 400, got.Height)
}

func (hs *harness) notifyWMName(win uint32) {
	hs.t.Helper()
	before := hs.x.Stats().Replies
	hs.p.propertyNotify(win, uint32(xproto.AtomWmName), false)
	readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Replies > before })
}
