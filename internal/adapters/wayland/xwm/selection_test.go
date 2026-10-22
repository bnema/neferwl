package xwm

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"
	"time"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const peerXFixesFirstEvent = 87

// xfixesSelection sends an XFixesSelectionNotify.
func (p *xpeer) xfixesSelection(owner, selection uint32) {
	p.event(peerXFixesFirstEvent, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], owner)
		binary.LittleEndian.PutUint32(b[12:], selection)
	})
}

// selectionNotify sends a core SelectionNotify to the requestor.
func (p *xpeer) selectionNotify(requestor, selection, target, property uint32) {
	p.event(31, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[8:], requestor)
		binary.LittleEndian.PutUint32(b[12:], selection)
		binary.LittleEndian.PutUint32(b[16:], target)
		binary.LittleEndian.PutUint32(b[20:], property)
	})
}

// pumpReq dispatches X events until a request matching match was sent after
// the first skip requests.
func pumpReq(t *testing.T, x *XWM, p *xpeer, skip int, match func(xpeerReq) bool) (xpeerReq, int) {
	t.Helper()
	var found xpeerReq
	next := skip
	readUntil(t, x, func() bool {
		x.flush()
		reqs := p.requests()
		for i := skip; i < len(reqs); i++ {
			if match(reqs[i]) {
				found, next = reqs[i], i+1
				return true
			}
		}
		return false
	})
	return found, next
}

func TestSelectionBoot(t *testing.T) {
	x, p := bootPeer(t, nil)
	require.NotZero(t, x.selWin)
	reqs := p.requests()
	var created, selected int
	for _, r := range reqs {
		switch {
		case r.major == opCreateWindow && r.u32(4) == x.selWin:
			created++
			require.EqualValues(t, 2, binary.LittleEndian.Uint16(r.raw[22:]), "InputOnly")
		case r.major == 138 && r.minor == 2: // SelectSelectionInput
			selected++
			require.EqualValues(t, peerRoot, r.u32(4))
			require.Contains(t, []uint32{p.atom("CLIPBOARD"), p.atom("PRIMARY")}, r.u32(8))
			require.EqualValues(t, 7, r.u32(12))
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 2, selected)
}

func TestSelectionXOwnsAnnounces(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const owner = 0x500010
	clip, prim := p.atom("CLIPBOARD"), p.atom("PRIMARY")
	tgt := p.atom("TARGETS")

	// Owner with text targets: TARGETS probe, then the intersection.
	p.xfixesSelection(owner, clip)
	req, n := pumpReq(t, x, p, 0, func(r xpeerReq) bool { return r.major == 24 }) // ConvertSelection
	require.Equal(t, x.selWin, req.u32(4))
	require.Equal(t, clip, req.u32(8))
	require.Equal(t, tgt, req.u32(12))
	prop := req.u32(16)
	require.Equal(t, p.atom("_NEFERWL_TARGETS"), prop)
	p.setProp(x.selWin, prop, 4, 32, le32(tgt, p.atom("UTF8_STRING"), p.atom("image/png"), p.atom("TEXT")))
	got := make(chan []string, 8)
	h.EXPECT().SelectionOwned(SelClipboard, []string{"text/plain;charset=utf-8", "UTF8_STRING", "TEXT"}).Run(func(int, []string) { got <- nil }).Once()
	p.selectionNotify(x.selWin, clip, tgt, prop)
	readUntil(t, x, func() bool { return len(got) == 1 })
	pumpReq(t, x, p, n, func(r xpeerReq) bool { return r.major == opGetProperty && r.u32(8) == prop })

	// Only image/png: nothing offered.
	p.xfixesSelection(owner, prim)
	pumpReq(t, x, p, n, func(r xpeerReq) bool { return r.major == 24 && r.u32(8) == prim })
	p.setProp(x.selWin, prop, 4, 32, le32(tgt, p.atom("image/png")))
	h.EXPECT().SelectionOwned(SelPrimary, []string(nil)).Run(func(int, []string) { got <- nil }).Once()
	p.selectionNotify(x.selWin, prim, tgt, prop)
	readUntil(t, x, func() bool { return len(got) == 2 })

	// Owner None.
	h.EXPECT().SelectionOwned(SelClipboard, []string(nil)).Run(func(int, []string) { got <- nil }).Once()
	p.xfixesSelection(0, clip)
	readUntil(t, x, func() bool { return len(got) == 3 })

	// Our own window: ignored (the mock fails on any host call).
	before := len(p.requests())
	p.xfixesSelection(x.selWin, clip)
	ev := x.Stats().Events
	readUntil(t, x, func() bool { return x.Stats().Events > ev })
	x.flush()
	require.Equal(t, before, len(p.requests()))
}

// ---- X -> Wayland ----

type selRig struct {
	x     *XWM
	p     *xpeer
	h     *mockhost
	clip  uint32
	utf8  uint32
	data  uint32
	skip  int
	timer func()
}

// ownedRig attaches an XWM whose CLIPBOARD is owned by an X client offering text.
func ownedRig(t *testing.T) *selRig {
	t.Helper()
	x, p, h, _, _ := attached(t)
	r := &selRig{x: x, p: p, h: h, clip: p.atom("CLIPBOARD"), utf8: p.atom("UTF8_STRING"), data: p.atom("_NEFERWL_DATA")}
	h.EXPECT().After(mock.Anything, mock.Anything).Run(func(_ time.Duration, fn func()) { r.timer = fn }).Return(func() {}).Maybe()
	got := 0
	h.EXPECT().SelectionOwned(SelClipboard, mock.Anything).Run(func(int, []string) { got++ }).Once()
	p.xfixesSelection(0x500010, r.clip)
	_, n := pumpReq(t, x, p, 0, func(q xpeerReq) bool { return q.major == 24 })
	tgt := p.atom("TARGETS")
	p.setProp(x.selWin, p.atom("_NEFERWL_TARGETS"), 4, 32, le32(tgt, r.utf8))
	p.selectionNotify(x.selWin, r.clip, tgt, p.atom("_NEFERWL_TARGETS"))
	readUntil(t, x, func() bool { return got == 1 })
	r.skip = len(p.requests())
	_ = n
	return r
}

// receive starts a paste and returns the read end of its pipe.
func (r *selRig) receive(t *testing.T, size int) *os.File {
	t.Helper()
	fds := make([]int, 2)
	require.NoError(t, unix.Pipe2(fds, unix.O_CLOEXEC))
	if size > 0 {
		_, err := unix.FcntlInt(uintptr(fds[0]), unix.F_SETPIPE_SZ, size)
		require.NoError(t, err)
	}
	r.x.SelectionReceive(SelClipboard, "text/plain;charset=utf-8", fds[1])
	r.x.flush()
	f := os.NewFile(uintptr(fds[0]), "pipe-r")
	t.Cleanup(func() { f.Close() })
	return f
}

func (r *selRig) waitGet(t *testing.T) {
	t.Helper()
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == opGetProperty && q.u32(8) == r.data })
	r.skip = n
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func TestSelectionXToWaylandSmall(t *testing.T) {
	r := ownedRig(t)
	f := r.receive(t, 0)
	req, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	require.Equal(t, r.x.selWin, req.u32(4))
	require.Equal(t, r.clip, req.u32(8))
	require.Equal(t, r.utf8, req.u32(12))
	require.Equal(t, r.data, req.u32(16))
	r.skip = n
	r.p.setProp(r.x.selWin, r.data, r.utf8, 8, []byte("hello clipboard"))
	r.p.selectionNotify(r.x.selWin, r.clip, r.utf8, r.data)
	r.waitGet(t)
	buf := make([]byte, 64)
	var got []byte
	readUntil(t, r.x, func() bool {
		m, _ := unix.Read(int(f.Fd()), buf[:0:0])
		_ = m
		return r.x.sel.in[0].state == inFree
	})
	m, err := f.Read(buf)
	require.NoError(t, err)
	got = buf[:m]
	require.Equal(t, "hello clipboard", string(got))
	_, ok := r.p.prop(r.x.selWin, "_NEFERWL_DATA")
	require.False(t, ok, "property deleted by the read")
	_, err = f.Read(buf)
	require.ErrorIs(t, err, io.EOF, "write end closed")
}

func TestSelectionXToWaylandRefused(t *testing.T) {
	r := ownedRig(t)
	f := r.receive(t, 0)
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.skip = n
	r.p.selectionNotify(r.x.selWin, r.clip, r.utf8, 0)
	readUntil(t, r.x, func() bool { return r.x.sel.in[0].state == inFree })
	_, err := f.Read(make([]byte, 8))
	require.ErrorIs(t, err, io.EOF)
}

func TestSelectionXToWaylandINCR(t *testing.T) {
	r := ownedRig(t)
	const total = 1 << 20
	want := pattern(total)
	f := r.receive(t, 1<<20)
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.skip = n
	r.p.setProp(r.x.selWin, r.data, r.p.atom("INCR"), 32, le32(total))
	r.p.selectionNotify(r.x.selWin, r.clip, r.utf8, r.data)
	r.waitGet(t) // reads and deletes the INCR marker
	gets := 1
	for off := 0; off <= total; off += chunk {
		end := min(off+chunk, total)
		r.p.setProp(r.x.selWin, r.data, r.utf8, 8, want[off:end])
		r.p.propertyNotify(r.x.selWin, r.data, false)
		r.waitGet(t)
		gets++
	}
	readUntil(t, r.x, func() bool { return r.x.sel.in[0].state == inFree })
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got), "byte-exact, got %d", len(got))
	require.Equal(t, 1+total/chunk+1, gets, "marker, 16 chunks, empty terminator")
}

// A reader that does not drain: no further GetProperty until the pipe has room.
func TestSelectionXToWaylandBackpressure(t *testing.T) {
	r := ownedRig(t)
	src := newMockfdSource(t)
	var onW func()
	r.x.lp = newFDLoop(t, r, src, nil, &onW)
	src.EXPECT().SetWritable(true).Return(nil).Once()
	src.EXPECT().SetWritable(false).Return(nil).Once()
	src.EXPECT().Remove().Return().Once()
	f := r.receive(t, 0) // default 64 KiB pipe
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.skip = n
	r.p.setProp(r.x.selWin, r.data, r.p.atom("INCR"), 32, le32(3*chunk))
	r.p.selectionNotify(r.x.selWin, r.clip, r.utf8, r.data)
	r.waitGet(t)
	want := pattern(3 * chunk)
	chunkN := func(k int) {
		r.p.setProp(r.x.selWin, r.data, r.utf8, 8, want[k*chunk:(k+1)*chunk])
		r.p.propertyNotify(r.x.selWin, r.data, false)
	}
	chunkN(0) // fills the pipe exactly
	r.waitGet(t)
	readUntil(t, r.x, func() bool { return r.x.sel.in[0].state == inWaitIncr })
	chunkN(1) // nobody reads: EAGAIN, the buffer stays pending
	r.waitGet(t)
	readUntil(t, r.x, func() bool { return r.x.sel.in[0].state == inWriting })
	require.NotNil(t, onW)

	gets := getProps(r.p, 0, r.x.selWin)
	chunkN(2) // announced while a buffer is pending
	ev := r.x.Stats().Events
	readUntil(t, r.x, func() bool { return r.x.Stats().Events > ev })
	r.x.flush()
	require.Equal(t, gets, getProps(r.p, 0, r.x.selWin), "no GetProperty while a buffer is pending")

	got := make([]byte, 0, 3*chunk)
	buf := make([]byte, chunk)
	m, err := f.Read(buf) // the reader wakes: room again
	require.NoError(t, err)
	got = append(got, buf[:m]...)
	onW()
	require.Equal(t, inReading, r.x.sel.in[0].state, "the pending announcement is served once drained")
	r.waitGet(t)
	_ = got
	r.x.selClose()
}

func TestSelectionTimeouts(t *testing.T) {
	r := ownedRig(t)
	f := r.receive(t, 0)
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.skip = n
	require.NotNil(t, r.timer)
	// X stays silent after ConvertSelection: nine ticks keep it alive, the tenth aborts.
	for i := 0; i < xIdleMax-1; i++ {
		fn := r.timer
		fn()
		require.Equal(t, inConverting, r.x.sel.in[0].state, i)
	}
	r.timer()
	require.Equal(t, inFree, r.x.sel.in[0].state)
	_, err := f.Read(make([]byte, 8))
	require.ErrorIs(t, err, io.EOF, "fd closed")
	pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == opDeleteProperty && q.u32(8) == r.data })
	require.Equal(t, nXfer, r.x.sel.pool.n)
}

func TestSelectionPoolExhausted(t *testing.T) {
	r := ownedRig(t)
	var files []*os.File
	for i := 0; i < nXfer; i++ {
		files = append(files, r.receive(t, 0))
	}
	fifth := r.receive(t, 0)
	_, err := fifth.Read(make([]byte, 8))
	require.ErrorIs(t, err, io.EOF, "the fifth paste is refused with its pipe closed")
	for i := range r.x.sel.in {
		require.NotEqual(t, inFree, r.x.sel.in[i].state, i)
	}
	_ = files
	r.x.selClose()
	for i := range r.x.sel.in {
		require.Equal(t, inFree, r.x.sel.in[i].state, i)
	}
}

// allocRig is a rig whose peer sinks requests and whose timer seam is inert.
func allocRig(t *testing.T) *selRig {
	r := ownedRig(t)
	sinkPeer(t, r.x, r.p)
	r.x.lp = newTimerLoop(t)
	r.x.sel.timerOn = true // no timer re-arming in the measured loop
	return r
}

func dataReply(r *selRig, typ uint32, payload []byte) *xturbo.Reply {
	raw := make([]byte, 32+(len(payload)+3)&^3)
	raw[0], raw[1] = 1, 8
	binary.LittleEndian.PutUint32(raw[8:], typ)
	binary.LittleEndian.PutUint32(raw[16:], uint32(len(payload)))
	copy(raw[32:], payload)
	return &xturbo.Reply{Kind: kindSelData, Raw: raw}
}

func TestSelectionCopyAllocations(t *testing.T) {
	r := allocRig(t)
	x := r.x
	fds := make([]int, 2)
	require.NoError(t, unix.Pipe2(fds, unix.O_CLOEXEC))
	t.Cleanup(func() { unix.Close(fds[0]); unix.Close(fds[1]) })
	payload := pattern(4096)
	rep := dataReply(r, r.utf8, payload)
	var note xturbo.Event
	note.Kind = xproto.KindSelectionNotify
	binary.LittleEndian.PutUint32(note.Raw[8:], x.selWin)
	binary.LittleEndian.PutUint32(note.Raw[12:], r.clip)
	binary.LittleEndian.PutUint32(note.Raw[16:], r.utf8)
	binary.LittleEndian.PutUint32(note.Raw[20:], r.data)
	sink := make([]byte, 8192)
	mime := textMimes[0]
	round := func() {
		fd, err := unix.FcntlInt(uintptr(fds[1]), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			t.Fatal("dup")
		}
		x.SelectionReceive(SelClipboard, mime, fd)
		x.onSelectionNotify(&note)
		rep.Args = [4]uint32{0, x.sel.in[0].gen}
		x.onSelDataReply(rep)
		if m, err := unix.Read(fds[0], sink); err != nil || m != 4096 {
			t.Fatal("read")
		}
		x.posted = false
		x.flush()
	}
	round()
	require.Zero(t, testing.AllocsPerRun(100, round))
}

func TestSelectionINCRChunkAllocations(t *testing.T) {
	r := allocRig(t)
	x := r.x
	fds := make([]int, 2)
	require.NoError(t, unix.Pipe2(fds, unix.O_CLOEXEC))
	t.Cleanup(func() { unix.Close(fds[0]); unix.Close(fds[1]) })
	x.SelectionReceive(SelClipboard, textMimes[0], fds[1])
	var note xturbo.Event
	note.Kind = xproto.KindSelectionNotify
	binary.LittleEndian.PutUint32(note.Raw[8:], x.selWin)
	binary.LittleEndian.PutUint32(note.Raw[12:], r.clip)
	binary.LittleEndian.PutUint32(note.Raw[16:], r.utf8)
	binary.LittleEndian.PutUint32(note.Raw[20:], r.data)
	x.onSelectionNotify(&note)
	x.onSelDataReply(dataReply(r, r.p.atom("INCR"), le32(1<<20)))
	require.Equal(t, inWaitIncr, x.sel.in[0].state)
	rep := dataReply(r, r.utf8, pattern(chunk))
	var pn xturbo.Event
	pn.Kind = xproto.KindPropertyNotify
	binary.LittleEndian.PutUint32(pn.Raw[4:], x.selWin)
	binary.LittleEndian.PutUint32(pn.Raw[8:], r.data)
	sink := make([]byte, chunk)
	step := func() {
		x.onSelProperty(&pn)
		rep.Args = [4]uint32{0, x.sel.in[0].gen}
		x.onSelDataReply(rep)
		if m, err := unix.Read(fds[0], sink); err != nil || m != chunk {
			t.Fatal("read")
		}
		x.posted = false
		x.flush()
	}
	step()
	require.Zero(t, testing.AllocsPerRun(100, step))
	require.Equal(t, inWaitIncr, x.sel.in[0].state)
}

// ---- Wayland -> X ----

const reqWin = 0x500020

func (p *xpeer) selectionRequest(owner, requestor, selection, target, property uint32) {
	p.event(30, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[8:], owner)
		binary.LittleEndian.PutUint32(b[12:], requestor)
		binary.LittleEndian.PutUint32(b[16:], selection)
		binary.LittleEndian.PutUint32(b[20:], target)
		binary.LittleEndian.PutUint32(b[24:], property)
	})
}

func (p *xpeer) selectionClear(owner, selection uint32) {
	p.event(29, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[8:], owner)
		binary.LittleEndian.PutUint32(b[12:], selection)
	})
}

// sentNotify returns the property of the SelectionNotify SendEvent after skip.
func sentNotify(t *testing.T, r *selRig) (property uint32) {
	t.Helper()
	req, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == opSendEvent && q.raw[12] == 31 })
	r.skip = n
	require.EqualValues(t, reqWin, req.u32(4))
	require.Equal(t, r.clip, binary.LittleEndian.Uint32(req.raw[12+12:]))
	return binary.LittleEndian.Uint32(req.raw[12+20:])
}

// ownedByWayland attaches an XWM and gives CLIPBOARD to a Wayland client offering mimes.
func ownedByWayland(t *testing.T, mimes ...string) *selRig {
	t.Helper()
	x, p, h, _, _ := attached(t)
	r := &selRig{x: x, p: p, h: h, clip: p.atom("CLIPBOARD"), utf8: p.atom("UTF8_STRING"), data: p.atom("_NEFERWL_DATA")}
	h.EXPECT().After(mock.Anything, mock.Anything).Run(func(_ time.Duration, fn func()) { r.timer = fn }).Return(func() {}).Maybe()
	x.WaylandSelection(SelClipboard, mimes)
	x.flush()
	req, n := pumpReq(t, x, p, 0, func(q xpeerReq) bool { return q.major == opSetSelectionOwner && q.u32(8) == r.clip })
	require.Equal(t, x.selWin, req.u32(4))
	r.skip = n
	return r
}

// serve queues a SelectionRequest; SelectionRead hands out the read end of a
// pipe whose write end is returned.
func (r *selRig) serve(t *testing.T, target uint32) (w *os.File) {
	t.Helper()
	fds := make([]int, 2)
	require.NoError(t, unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK))
	r.h.EXPECT().SelectionRead(SelClipboard, mock.Anything).Return(fds[0]).Once()
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, target, r.data)
	w = os.NewFile(uintptr(fds[1]), "pipe-w")
	t.Cleanup(func() { w.Close() })
	return w
}

func TestSelectionWaylandToXTargets(t *testing.T) {
	r := ownedByWayland(t, "text/plain")
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, r.p.atom("TARGETS"), r.data)
	require.Equal(t, r.data, sentNotify(t, r))
	v, ok := r.p.prop(reqWin, "_NEFERWL_DATA")
	require.True(t, ok)
	require.Equal(t, []uint32{r.p.atom("TARGETS"), r.p.atom("TIMESTAMP"), r.p.atom("STRING")}, atomsOf(v), "no UTF8 target for text/plain only")
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, r.p.atom("image/png"), r.data)
	require.Zero(t, sentNotify(t, r), "unknown target refused")
	// UTF8_STRING is not offered by a text/plain-only source, but STRING reads it.
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, r.utf8, r.data)
	require.Zero(t, sentNotify(t, r), "no UTF-8 text offered")
}

func TestSelectionWaylandToXSmall(t *testing.T) {
	r := ownedByWayland(t, "text/plain;charset=utf-8")
	w := r.serve(t, r.utf8)
	data := pattern(4096)
	_, err := w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.Equal(t, r.data, sentNotify(t, r))
	v, ok := r.p.prop(reqWin, "_NEFERWL_DATA")
	require.True(t, ok)
	require.Equal(t, r.utf8, v.typ)
	require.True(t, bytes.Equal(data, v.data))
	require.Equal(t, nXfer, r.x.sel.pool.n)
}

func TestSelectionWaylandToXINCR(t *testing.T) {
	r := ownedByWayland(t, "text/plain;charset=utf-8")
	src := newMockfdSource(t)
	var onR func()
	removes := 0
	r.x.lp = newFDLoop(t, r, src, &onR, nil)
	src.EXPECT().Remove().Run(func() { removes++ }).Return().Maybe()
	const total = 1<<20 + 123
	data := pattern(total)
	w := r.serve(t, r.utf8)
	require.NoError(t, unix.SetNonblock(int(w.Fd()), false))
	go func() { _, _ = w.Write(data); w.Close() }()

	// The first fill is a whole chunk: INCR is announced.
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for !r.x.sel.out[0].incr {
		require.True(t, time.Now().Before(deadline), "INCR never announced")
		r.x.onReadable()
		if onR != nil && r.x.sel.out[0].state == outActive {
			onR()
		}
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, r.data, sentNotify(t, r))
	v, _ := r.p.prop(reqWin, "_NEFERWL_DATA")
	require.Equal(t, r.p.atom("INCR"), v.typ)
	mask, _ := pumpReq(t, r.x, r.p, 0, func(q xpeerReq) bool {
		return q.major == opChangeWindowAttributes && q.u32(4) == reqWin && q.u32(12) == uint32(xproto.EventMaskPropertyChange)
	})
	require.NotZero(t, mask.raw)

	chunks := 0
	for {
		// The requestor deletes the property: the next chunk goes out.
		r.p.propertyNotify(reqWin, r.data, true)
		before := len(r.p.requests())
		if onR != nil {
			time.Sleep(time.Millisecond)
		}
		var cp xpeerProp
		readUntil(t, r.x, func() bool {
			if onR != nil && r.x.sel.out[0].state != outFree && r.x.sel.out[0].n < chunk && !r.x.sel.out[0].eof {
				onR()
			}
			r.x.flush()
			for _, q := range r.p.requests()[before:] {
				if q.major == opChangeProperty && q.u32(4) == reqWin {
					return true
				}
			}
			return false
		})
		cp, _ = r.p.prop(reqWin, "_NEFERWL_DATA")
		chunks++
		if len(cp.data) == 0 {
			break
		}
		require.Equal(t, r.utf8, cp.typ)
		got = append(got, cp.data...)
	}
	require.True(t, bytes.Equal(data, got), "byte-exact: %d of %d", len(got), total)
	require.Equal(t, total/chunk+2, chunks, "16 full chunks, the tail, then the empty terminator")
	readUntil(t, r.x, func() bool { return r.x.sel.out[0].state == outFree })
	// The mask is removed at the end.
	reqs := r.p.requests()
	last := reqs[len(reqs)-1]
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].major == opChangeWindowAttributes && reqs[i].u32(4) == reqWin {
			last = reqs[i]
			break
		}
	}
	require.Zero(t, last.u32(12), "event mask dropped")
	require.Equal(t, nXfer, r.x.sel.pool.n)
}

func TestSelectionWaylandToXTimeoutAndClear(t *testing.T) {
	r := ownedByWayland(t, "text/plain;charset=utf-8")
	src := newMockfdSource(t)
	src.EXPECT().Remove().Return().Maybe()
	r.x.lp = newFDLoop(t, r, src, nil, nil)
	w := r.serve(t, r.utf8)
	_, err := w.Write(pattern(chunk)) // a full chunk: INCR
	require.NoError(t, err)
	readUntil(t, r.x, func() bool { return r.x.sel.out[0].incr })
	require.Equal(t, r.data, sentNotify(t, r))
	require.NotNil(t, r.timer)
	for i := 0; i < xIdleMax; i++ {
		require.Equal(t, outActive, r.x.sel.out[0].state, i)
		r.timer()
	}
	require.Equal(t, outFree, r.x.sel.out[0].state, "no Deleted for 10 s aborts")
	pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == opDeleteProperty && q.u32(4) == reqWin })

	// SelectionClear aborts a running transfer and refuses further requests.
	w2 := r.serve(t, r.utf8)
	_, err = w2.Write(pattern(chunk))
	require.NoError(t, err)
	readUntil(t, r.x, func() bool { return r.x.sel.out[0].state == outActive })
	r.p.selectionClear(r.x.selWin, r.clip)
	readUntil(t, r.x, func() bool { return r.x.sel.out[0].state == outFree })
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, r.utf8, r.data)
	require.Equal(t, r.data, sentNotify(t, r), "INCR announcement of the aborted transfer")
	require.Zero(t, sentNotify(t, r), "request after the clear is refused")
}

// Replacing the Wayland selection mid-transfer aborts it; nil releases X ownership.
func TestSelectionClearAndRace(t *testing.T) {
	r := ownedByWayland(t, "text/plain;charset=utf-8")
	src := newMockfdSource(t)
	src.EXPECT().Remove().Return().Maybe()
	r.x.lp = newFDLoop(t, r, src, nil, nil)
	w := r.serve(t, r.utf8)
	_, err := w.Write(pattern(chunk))
	require.NoError(t, err)
	readUntil(t, r.x, func() bool { return r.x.sel.out[0].incr })
	r.x.WaylandSelection(SelClipboard, []string{"text/plain"})
	require.Equal(t, outFree, r.x.sel.out[0].state)
	r.x.WaylandSelection(SelClipboard, nil) // protected or cleared
	r.x.flush()
	req, _ := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == opSetSelectionOwner && q.u32(4) == 0 })
	require.Equal(t, r.clip, req.u32(8), "ownership released")
	require.False(t, r.x.sel.st[SelClipboard].ours)
}

func TestSelectionCopyAllocationsWaylandToX(t *testing.T) {
	r := ownedByWayland(t, "text/plain;charset=utf-8")
	x := r.x
	sinkPeer(t, x, r.p)
	x.lp = newTimerLoop(t)
	x.sel.timerOn = true
	data := pattern(4096)
	var ev xturbo.Event
	ev.Kind = xproto.KindSelectionRequest
	binary.LittleEndian.PutUint32(ev.Raw[8:], x.selWin)
	binary.LittleEndian.PutUint32(ev.Raw[12:], reqWin)
	binary.LittleEndian.PutUint32(ev.Raw[16:], r.clip)
	binary.LittleEndian.PutUint32(ev.Raw[20:], r.utf8)
	binary.LittleEndian.PutUint32(ev.Raw[24:], r.data)
	fds := make([]int, 2)
	round := func() {
		if unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK) != nil {
			t.Fatal("pipe")
		}
		if _, err := unix.Write(fds[1], data); err != nil {
			t.Fatal("write")
		}
		unix.Close(fds[1])
		x.host.(*mockhost).ExpectedCalls = nil
		x.onSelectionRequest(&ev)
		x.posted = false
		x.flush()
	}
	_ = round
	// The host mock's matching is not allocation free, so the host call is replaced.
	x.sel.st[SelClipboard].wlMime = [len(textMimes)]string{"text/plain;charset=utf-8"}
	allocs := testing.AllocsPerRun(100, func() {
		if unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK) != nil {
			t.Fatal("pipe")
		}
		if _, err := unix.Write(fds[1], data); err != nil {
			t.Fatal("write")
		}
		unix.Close(fds[1])
		x.startOut(0, SelClipboard, reqWin, r.data, r.utf8, 0, fds[0])
		x.posted = false
		x.flush()
	})
	// Pipe2 itself does not allocate; the measured path is read + ChangeProperty + SendEvent.
	require.Zero(t, allocs)
	require.Equal(t, outFree, x.sel.out[0].state)
}

// A TARGETS reply that arrives after Wayland took the selection is dropped.
func TestSelectionStaleTargetsAfterWaylandOwner(t *testing.T) {
	x, p, h, _, _ := attached(t)
	const owner = 0x500010
	clip := p.atom("CLIPBOARD")
	tgt := p.atom("TARGETS")
	p.xfixesSelection(owner, clip)
	req, _ := pumpReq(t, x, p, 0, func(r xpeerReq) bool { return r.major == 24 })
	prop := req.u32(16)
	x.WaylandSelection(SelClipboard, []string{"text/plain"})
	p.setProp(x.selWin, prop, 4, 32, le32(tgt, p.atom("UTF8_STRING")))
	p.selectionNotify(x.selWin, clip, tgt, prop) // the mock fails on SelectionOwned
	ev := x.Stats().Events
	readUntil(t, x, func() bool { return x.Stats().Events > ev })
	x.flush()
	require.True(t, x.sel.st[SelClipboard].ours)
	for i := 0; i < 20; i++ { // let any (wrong) GetProperty round trip finish
		x.onReadable()
		x.flush()
		time.Sleep(time.Millisecond)
	}
	require.Zero(t, getProps(p, 0, x.selWin), "the stale TARGETS reply is not fetched")
	_ = h
}

// newFDLoop is a loop whose AddFD hands out src and captures the callbacks;
// timers are inert.
func newFDLoop(t *testing.T, rig *selRig, src fdSource, onR, onW *func()) *mockloop {
	l := newMockloop(t)
	l.EXPECT().AddFD(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ int, r, w func()) (fdSource, error) {
			if onR != nil {
				*onR = r
			}
			if onW != nil {
				*onW = w
			}
			return src, nil
		}).Maybe()
	l.EXPECT().After(mock.Anything, mock.Anything).RunAndReturn(
		func(_ time.Duration, fn func()) func() { rig.timer = fn; return func() {} }).Maybe()
	return l
}

// newTimerLoop is a loop with inert timers.
func newTimerLoop(t *testing.T) *mockloop {
	l := newMockloop(t)
	l.EXPECT().After(mock.Anything, mock.Anything).RunAndReturn(
		func(time.Duration, func()) func() { return func() {} }).Maybe()
	return l
}

// A late SelectionNotify of an aborted transfer (other selection or property)
// is not taken by the next transfer.
func TestSelectionLateReplyIgnored(t *testing.T) {
	r := ownedRig(t)
	_ = r.receive(t, 0)
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.skip = n
	for i := 0; i < xIdleMax; i++ {
		r.timer()
	}
	require.Equal(t, inFree, r.x.sel.in[0].state)
	_ = r.receive(t, 0)
	require.Equal(t, inConverting, r.x.sel.in[0].state)
	prim := r.p.atom("PRIMARY")
	r.p.selectionNotify(r.x.selWin, prim, r.utf8, r.data)
	r.p.selectionNotify(r.x.selWin, r.clip, r.utf8, r.p.atom("_NEFERWL_TARGETS"))
	ev := r.x.Stats().Events
	readUntil(t, r.x, func() bool { return r.x.Stats().Events >= ev+2 })
	require.Equal(t, inConverting, r.x.sel.in[0].state)
}

// CloseConn closes the fds of in-flight transfers.
func TestSelectionCloseConnClosesFDs(t *testing.T) {
	r := ownedRig(t)
	f := r.receive(t, 0)
	r.x.CloseConn()
	_, err := f.Read(make([]byte, 8))
	require.ErrorIs(t, err, io.EOF)
}

// Closing never starts a queued transfer's ConvertSelection.
func TestSelectionCloseDoesNotStartQueued(t *testing.T) {
	r := ownedRig(t)
	_ = r.receive(t, 0)
	_ = r.receive(t, 0)
	_, n := pumpReq(t, r.x, r.p, r.skip, func(q xpeerReq) bool { return q.major == 24 })
	r.x.selClose()
	r.x.flush()
	time.Sleep(50 * time.Millisecond)
	cs := 0
	for _, q := range since(r.p, r.skip, 24) {
		cs++
		_ = q
	}
	_ = n
	require.Equal(t, 1, cs)
	require.False(t, r.x.sel.timerOn)
}

func TestSelectionWaylandToXTimestamp(t *testing.T) {
	r := ownedByWayland(t, "text/plain")
	r.p.selectionRequest(r.x.selWin, reqWin, r.clip, r.p.atom("TIMESTAMP"), r.data)
	require.Equal(t, r.data, sentNotify(t, r))
	v, ok := r.p.prop(reqWin, "_NEFERWL_DATA")
	require.True(t, ok)
	require.EqualValues(t, 32, v.format)
	require.EqualValues(t, 19, v.typ, "INTEGER")
	require.Len(t, v.data, 4)
}

func TestSelectionOwnershipTimeFromLastEvent(t *testing.T) {
	x, p, _, _, _ := attached(t)
	p.event(28, func(b *[32]byte) { // PropertyNotify
		binary.LittleEndian.PutUint32(b[4:], x.selWin)
		binary.LittleEndian.PutUint32(b[8:], 1)
		binary.LittleEndian.PutUint32(b[12:], 4242)
	})
	readUntil(t, x, func() bool { return x.lastTime == 4242 })
	x.WaylandSelection(SelClipboard, []string{"text/plain"})
	require.EqualValues(t, 4242, x.sel.st[SelClipboard].ownTime)
}
