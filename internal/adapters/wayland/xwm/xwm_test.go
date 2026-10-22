package xwm

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"

	"bnema.dev/xturbo/xproto"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func testLog() zerowrap.Logger { return zerowrap.Logger{Logger: zerolog.Nop()} }

func bootPeer(t *testing.T, mutate func(p *xpeer)) (*XWM, *xpeer) {
	t.Helper()
	fd, p := startXPeer(t, fullPeerExts())
	if mutate != nil {
		p.set(mutate)
	}
	x, err := Boot(fd, testLog())
	require.NoError(t, err)
	t.Cleanup(x.CloseConn)
	return x, p
}

func atomsOf(p xpeerProp) []uint32 {
	out := make([]uint32, len(p.data)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(p.data[4*i:])
	}
	return out
}

func TestBootRequests(t *testing.T) {
	x, p := bootPeer(t, nil)
	reqs := p.requests()

	first := -1
	for i, r := range reqs {
		if r.major == opInternAtom {
			first = i
			break
		}
	}
	require.NotEqual(t, -1, first)
	for i := 0; i < atomCount; i++ {
		r := reqs[first+i]
		require.EqualValues(t, opInternAtom, r.major, i)
		n := int(binary.LittleEndian.Uint16(r.raw[4:]))
		require.Equal(t, atomNames[i], string(r.raw[8:8+n]), i)
		require.Equal(t, p.atom(atomNames[i]), x.atoms[i])
	}

	// Root mask, then composite redirect, then the WM window and ownership.
	var rootMask, redirect, create, s0, cm, focus int
	rootMask, redirect, create, s0, cm, focus = -1, -1, -1, -1, -1, -1
	wm := x.wmWin
	for i := first + atomCount; i < len(reqs); i++ {
		r := reqs[i]
		switch {
		case r.major == opChangeWindowAttributes && r.u32(4) == peerRoot:
			rootMask = i
			require.EqualValues(t, 1<<11, r.u32(8))
			want := uint32(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify | xproto.EventMaskPropertyChange)
			require.Equal(t, want, r.u32(12))
		case r.major == 142 && r.minor == 2:
			redirect = i
			require.EqualValues(t, peerRoot, r.u32(4))
			require.EqualValues(t, 1, r.raw[8], "manual")
		case r.major == opCreateWindow:
			create = i
			require.Equal(t, wm, r.u32(4))
		case r.major == opSetSelectionOwner && r.u32(8) == p.atom("WM_S0"):
			s0 = i
			require.Equal(t, wm, r.u32(4))
		case r.major == opSetSelectionOwner && r.u32(8) == p.atom("_NET_WM_CM_S0"):
			cm = i
		case r.major == opSetInputFocus:
			focus = i
			require.EqualValues(t, 0, r.u32(4), "focus None")
		}
	}
	for name, i := range map[string]int{"rootMask": rootMask, "redirect": redirect, "create": create, "WM_S0": s0, "CM_S0": cm, "focus": focus} {
		require.NotEqual(t, -1, i, name)
	}
	require.Less(t, rootMask, redirect)
	require.Less(t, redirect, create)
	require.Less(t, create, s0)

	sup, ok := p.prop(peerRoot, "_NET_SUPPORTED")
	require.True(t, ok)
	var names []string
	byAtom := map[uint32]string{}
	for n, a := range p.atoms {
		byAtom[a] = n
	}
	for _, a := range atomsOf(sup) {
		names = append(names, byAtom[a])
	}
	require.Equal(t, []string{
		"_NET_WM_STATE", "_NET_ACTIVE_WINDOW", "_NET_CLOSE_WINDOW", "_NET_WM_STATE_FOCUSED",
		"_NET_WM_STATE_MODAL", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_HIDDEN", "_NET_CLIENT_LIST",
		"_NET_CLIENT_LIST_STACKING", "_NET_SUPPORTING_WM_CHECK", "_NET_WM_NAME", "_NET_WM_PID",
		"_NET_WM_WINDOW_TYPE",
	}, names)
	require.NotContains(t, names, "_NET_WM_MOVERESIZE")
	require.NotContains(t, names, "_NET_WM_STATE_MAXIMIZED_VERT")

	check, ok := p.prop(peerRoot, "_NET_SUPPORTING_WM_CHECK")
	require.True(t, ok)
	require.Equal(t, []uint32{wm}, atomsOf(check))
	check, ok = p.prop(wm, "_NET_SUPPORTING_WM_CHECK")
	require.True(t, ok)
	require.Equal(t, []uint32{wm}, atomsOf(check))
	name, ok := p.prop(wm, "_NET_WM_NAME")
	require.True(t, ok)
	require.Equal(t, "neferwl", string(name.data))
	active, ok := p.prop(peerRoot, "_NET_ACTIVE_WINDOW")
	require.True(t, ok)
	require.Equal(t, []uint32{0}, atomsOf(active))
	for _, l := range []string{"_NET_CLIENT_LIST", "_NET_CLIENT_LIST_STACKING"} {
		v, ok := p.prop(peerRoot, l)
		require.True(t, ok, l)
		require.Empty(t, v.data, l)
	}
}

func TestBootWMExists(t *testing.T) {
	fd, p := startXPeer(t, fullPeerExts())
	p.set(func(p *xpeer) { p.denyRoot = true })
	x, err := Boot(fd, testLog())
	require.ErrorIs(t, err, ErrWMExists)
	require.Nil(t, x)
	// Nothing the running window manager owns was touched.
	for _, r := range p.requests() {
		require.NotEqual(t, uint8(opSetSelectionOwner), r.major)
		if r.major == opChangeProperty {
			require.NotEqual(t, uint32(peerRoot), r.u32(4))
		}
	}
}

func TestBootNoComposite(t *testing.T) {
	exts := fullPeerExts()
	delete(exts, "Composite")
	fd, _ := startXPeer(t, exts)
	_, err := Boot(fd, testLog())
	require.ErrorIs(t, err, ErrNoComposite)
}

func TestBootOptionalExtensions(t *testing.T) {
	exts := fullPeerExts()
	delete(exts, "XFIXES")
	delete(exts, "X-Resource")
	fd, _ := startXPeer(t, exts)
	x, err := Boot(fd, testLog())
	require.NoError(t, err)
	x.CloseConn()
}

func TestBootConnectFailure(t *testing.T) {
	fd, p := startXPeer(t, fullPeerExts())
	p.hangup()
	_, err := Boot(fd, testLog())
	require.Error(t, err)
}

func TestBootExistingWindows(t *testing.T) {
	x, p := bootPeer(t, func(p *xpeer) {
		p.children = []uint32{0x400001, 0x400002}
		p.attrs[0x400002] = [2]byte{1, 2} // override-redirect, viewable
	})
	_ = p
	require.NotZero(t, x.Slot(0x400001))
	require.NotZero(t, x.Slot(0x400002))
	require.Equal(t, kindManaged, x.tab.rec(x.Slot(0x400001)).kind)
	require.Equal(t, kindUnmanaged, x.tab.rec(x.Slot(0x400002)).kind)
}

// readUntil dispatches the socket until cond holds.
func readUntil(t *testing.T, x *XWM, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		x.onReadable()
		time.Sleep(time.Millisecond)
	}
}

func newPost() (post func(func()), runs *[]func()) {
	var q []func()
	return func(f func()) { q = append(q, f) }, &q
}

func TestAttachAnnouncesWindowsFoundAtBoot(t *testing.T) {
	x, _ := bootPeer(t, func(p *xpeer) {
		p.children = []uint32{0x400001, 0x400002}
		p.attrs[0x400002] = [2]byte{1, 2}
	})
	h := newMockhost(t)
	src := newMockfdSource(t)
	h.EXPECT().Created(x.Slot(0x400001), uint32(0x400001)).Return(ports.WindowID(7)).Once()
	post, _ := newPost()
	x.attach(src, h, post)
	require.Equal(t, ports.WindowID(7), x.tab.rec(x.Slot(0x400001)).id)
}

func attached(t *testing.T) (*XWM, *xpeer, *mockhost, *mockfdSource, *[]func()) {
	t.Helper()
	x, p := bootPeer(t, nil)
	h := newMockhost(t)
	src := newMockfdSource(t)
	post, runs := newPost()
	x.attach(src, h, post)
	return x, p, h, src, runs
}

func TestCreateAndDestroyManaged(t *testing.T) {
	x, p, h, src, _ := attached(t)
	const win = 0x500001
	h.EXPECT().Created(mock.Anything, uint32(win)).Return(ports.WindowID(3)).Once()
	p.createNotify(win, false)
	readUntil(t, x, func() bool { return x.Slot(win) != 0 })
	s := x.Slot(win)
	require.Equal(t, kindManaged, x.tab.rec(s).kind)
	require.Equal(t, ports.WindowID(3), x.tab.rec(s).id)

	// The window is watched for property and focus changes.
	req, _ := p.waitReq(0, func(r xpeerReq) bool { return r.major == opChangeWindowAttributes && r.u32(4) == win })
	require.EqualValues(t, xproto.EventMaskPropertyChange|xproto.EventMaskFocusChange, req.u32(12))

	h.EXPECT().Dissociate(s, true).Once()
	p.destroyNotify(win)
	readUntil(t, x, func() bool { return x.Slot(win) == 0 })
	require.Zero(t, x.tab.n)
	_ = src
}

// The X server reports the XWM's own check window to it like any other
// child of the root; it is never adopted.
func TestOwnWindowNeverAdopted(t *testing.T) {
	x, p, _, _, _ := attached(t) // the mock fails the test on any host call
	p.createNotify(x.wmWin, false)
	const marker = 0x500003
	p.createNotify(marker, true)
	readUntil(t, x, func() bool { return x.Slot(marker) != 0 })
	require.Zero(t, x.Slot(x.wmWin))
}

func TestOverrideRedirectNeverReachesHost(t *testing.T) {
	x, p, _, _, _ := attached(t) // the mock fails the test on any host call
	p.createNotify(0x500002, true)
	readUntil(t, x, func() bool { return x.Slot(0x500002) != 0 })
	require.Equal(t, kindUnmanaged, x.tab.rec(x.Slot(0x500002)).kind)
	require.EqualValues(t, 1, x.Stats().UnmanagedSeen)
	p.destroyNotify(0x500002)
	readUntil(t, x, func() bool { return x.Slot(0x500002) == 0 })
}

func TestCloseDissociatesEverything(t *testing.T) {
	x, p, h, src, _ := attached(t)
	for _, win := range []uint32{0x500001, 0x500002} {
		h.EXPECT().Created(mock.Anything, win).Return(ports.WindowID(win)).Once()
		p.createNotify(win, false)
	}
	p.createNotify(0x500003, true)
	readUntil(t, x, func() bool { return x.Slot(0x500003) != 0 })
	h.EXPECT().Dissociate(x.Slot(0x500001), true).Once()
	h.EXPECT().Dissociate(x.Slot(0x500002), true).Once()
	src.EXPECT().Remove().Once()
	x.Close()
	x.Close() // idempotent
	require.True(t, x.closed)
	require.Equal(t, -1, x.conn.Fd())
}

func TestEOFClosesXWM(t *testing.T) {
	x, p, h, src, _ := attached(t)
	h.EXPECT().Created(mock.Anything, uint32(0x500001)).Return(ports.WindowID(1)).Once()
	p.createNotify(0x500001, false)
	readUntil(t, x, func() bool { return x.Slot(0x500001) != 0 })
	h.EXPECT().Dissociate(x.Slot(0x500001), true).Once()
	src.EXPECT().Remove().Once()
	p.hangup()
	readUntil(t, x, func() bool { return x.closed })
}

func TestWakeCoalescesFlush(t *testing.T) {
	x, _, _, _, runs := attached(t)
	x.wake()
	x.wake()
	x.wake()
	require.Len(t, *runs, 1)
	(*runs)[0]()
	x.wake()
	require.Len(t, *runs, 2)
}

func TestWriteBackpressure(t *testing.T) {
	x, p, _, src, _ := attached(t)
	p.pause()
	b := x.conn.Buf()
	big := make([]byte, 64<<10)
	for i := 0; i < 64; i++ { // 4 MiB: more than any socket buffer
		xproto.ChangeProperty(b, xproto.PropModeReplace, xproto.Window(x.root), xproto.AtomWmName, xproto.AtomString, 8, uint32(len(big)), big)
	}
	src.EXPECT().SetWritable(true).Return(nil).Once()
	x.flush()
	require.True(t, x.writable)
	x.flush() // still blocked: SetWritable is not called again
	x.onWritable()
	require.True(t, x.writable)

	drained := false
	src.EXPECT().SetWritable(false).RunAndReturn(func(bool) error { drained = true; return nil }).Once()
	p.resume()
	deadline := time.Now().Add(10 * time.Second)
	for !drained {
		require.True(t, time.Now().Before(deadline), "never drained")
		x.onWritable()
		time.Sleep(time.Millisecond)
	}
	require.False(t, x.writable)
}

func TestFlushWithNothingQueuedSendsNothing(t *testing.T) {
	x, p, _, _, _ := attached(t)
	n := len(p.requests())
	x.flush()
	time.Sleep(20 * time.Millisecond)
	require.Len(t, p.requests(), n, "nothing queued, nothing sent")
}

// The XWM never blocks after Boot: NewXID, Sync and InternAtoms are startup
// only, so they may appear in production code only inside boot().
func TestNoBlockingCallsOutsideBoot(t *testing.T) {
	files, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		text := string(src)
		lo, hi := 0, 0
		if name == "xwm.go" {
			lo = strings.Index(text, "func (x *XWM) boot() error")
			hi = strings.Index(text, "// Attach registers")
			require.Greater(t, hi, lo)
		}
		for _, call := range []string{".NewXID(", ".Sync(", ".InternAtoms("} {
			for _, at := range allIndex(text, call) {
				require.True(t, name == "xwm.go" && at > lo && at < hi, "%s: %s outside boot", name, call)
			}
		}
	}
}

func allIndex(s, sub string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(s[off:], sub)
		if i < 0 {
			return out
		}
		out = append(out, off+i)
		off += i + len(sub)
	}
}
