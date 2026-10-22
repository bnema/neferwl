package xwm

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Opcodes the peer understands.
const (
	opCreateWindow           = 1
	opChangeWindowAttributes = 2
	opGetWindowAttributes    = 3
	opMapWindow              = 8
	opConfigureWindow        = 12
	opGetGeometry            = 14
	opQueryTree              = 15
	opInternAtom             = 16
	opChangeProperty         = 18
	opGetProperty            = 20
	opSetSelectionOwner      = 22
	opSendEvent              = 25
	opSetInputFocus          = 42
	opGetInputFocus          = 43
	opQueryExtension         = 98
	opKillClient             = 113
)

const (
	peerRoot     = 0x100
	peerXIDBase  = 0x200000
	peerXIDMask  = 0x1fffff
	peerMaxWords = 0x100000
)

type xpeerExt struct{ major, firstEvent, firstError uint8 }

// xpeerReq is one request the peer received.
type xpeerReq struct {
	seq   uint16
	major uint8
	minor uint8
	raw   []byte // whole request, header included
}

func (r xpeerReq) u32(off int) uint32 { return binary.LittleEndian.Uint32(r.raw[off:]) }

type xpeerProp struct {
	typ    uint32
	format uint8
	data   []byte
}

type xpeerGeom struct {
	x, y         int16
	w, h, border uint16
}

// xpeer is a scripted X server speaking the real wire protocol over a
// socketpair: setup, extension queries, atoms, properties, geometry, window
// tree. It records every request. It is a protocol peer, not a test double of
// a project interface.
type xpeer struct {
	t *testing.T
	f *os.File

	mu       sync.Mutex
	seq      uint16
	reqs     []xpeerReq
	exts     map[string]xpeerExt
	atoms    map[string]uint32
	nextAtom uint32
	props    map[uint32]map[uint32]xpeerProp
	geoms    map[uint32]xpeerGeom
	attrs    map[uint32][2]byte // window -> {overrideRedirect, mapState}
	children []uint32
	denyRoot bool // answer the root event-mask request with BadAccess

	gate atomic.Pointer[chan struct{}] // closed = reading allowed
	done chan struct{}
}

func fullPeerExts() map[string]xpeerExt {
	return map[string]xpeerExt{
		"BIG-REQUESTS": {133, 0, 0},
		"XC-MISC":      {136, 0, 0},
		"Composite":    {142, 0, 0},
		"XFIXES":       {138, 87, 140},
		"X-Resource":   {144, 0, 0},
	}
}

// startXPeer returns the client end of a socketpair, to give to Boot, and the
// peer serving the other end.
func startXPeer(t *testing.T, exts map[string]xpeerExt) (int, *xpeer) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &xpeer{
		t: t, f: os.NewFile(uintptr(fds[1]), "xpeer"),
		exts: exts, atoms: map[string]uint32{}, nextAtom: 1000,
		props: map[uint32]map[uint32]xpeerProp{}, geoms: map[uint32]xpeerGeom{}, attrs: map[uint32][2]byte{},
		done: make(chan struct{}),
	}
	open := make(chan struct{})
	close(open)
	p.gate.Store(&open)
	t.Cleanup(func() {
		p.f.Close()
		<-p.done
	})
	go p.serve()
	return fds[0], p
}

func (p *xpeer) setupReply() []byte {
	vendor := []byte("xpeer\x00\x00\x00")
	b := make([]byte, 40+len(vendor)+40)
	b[0], b[2] = 1, 11
	binary.LittleEndian.PutUint16(b[6:], uint16((len(b)-8)/4))
	binary.LittleEndian.PutUint32(b[12:], peerXIDBase)
	binary.LittleEndian.PutUint32(b[16:], peerXIDMask)
	binary.LittleEndian.PutUint16(b[24:], 5)
	b[28] = 1
	copy(b[40:], vendor)
	scr := b[40+len(vendor):]
	binary.LittleEndian.PutUint32(scr, peerRoot)
	binary.LittleEndian.PutUint32(scr[32:], 0x22) // root visual
	scr[38] = 24                                  // root depth
	return b
}

func (p *xpeer) serve() {
	defer close(p.done)
	var hdr [12]byte
	if _, err := io.ReadFull(p.f, hdr[:]); err != nil {
		return
	}
	// Auth name and data are empty (NewConn sends none).
	if _, err := p.f.Write(p.setupReply()); err != nil {
		return
	}
	for {
		<-*p.gate.Load()
		var h [4]byte
		if _, err := io.ReadFull(p.f, h[:]); err != nil {
			return
		}
		raw := h[:]
		n := int(binary.LittleEndian.Uint16(h[2:])) * 4
		if n == 0 { // BIG-REQUESTS header
			var l [4]byte
			if _, err := io.ReadFull(p.f, l[:]); err != nil {
				return
			}
			raw = append(raw, l[:]...)
			n = int(binary.LittleEndian.Uint32(l[:])) * 4
		}
		rest := make([]byte, n-len(raw))
		if _, err := io.ReadFull(p.f, rest); err != nil {
			return
		}
		raw = append(append([]byte(nil), raw...), rest...)
		p.mu.Lock()
		p.seq++
		r := xpeerReq{p.seq, h[0], h[1], raw}
		p.reqs = append(p.reqs, r)
		p.answer(r, raw[len(raw)-len(rest):])
		p.mu.Unlock()
	}
}

// write sends bytes to the client. The caller holds p.mu.
func (p *xpeer) write(b []byte) { _, _ = p.f.Write(b) }

func (p *xpeer) reply(data uint8, body []byte) {
	words := (max(len(body), 24) - 24 + 3) / 4
	b := make([]byte, 32+4*words)
	b[0], b[1] = 1, data
	binary.LittleEndian.PutUint16(b[2:], p.seq)
	binary.LittleEndian.PutUint32(b[4:], uint32(words))
	copy(b[8:], body)
	p.write(b)
}

func (p *xpeer) errorPacket(code, major uint8, bad uint32) {
	b := make([]byte, 32)
	b[1] = code
	binary.LittleEndian.PutUint16(b[2:], p.seq)
	binary.LittleEndian.PutUint32(b[4:], bad)
	b[10] = major
	p.write(b)
}

func (p *xpeer) extName(major uint8) string {
	for name, e := range p.exts {
		if e.major == major {
			return name
		}
	}
	return ""
}

func le32(v ...uint32) []byte {
	b := make([]byte, 0, 4*len(v))
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, x)
	}
	return b
}

// answer runs with p.mu held.
func (p *xpeer) answer(r xpeerReq, body []byte) {
	switch {
	case r.major == opQueryExtension:
		e, ok := p.exts[string(body[4:4+binary.LittleEndian.Uint16(body)])]
		var present uint8
		if ok {
			present = 1
		}
		p.reply(0, []byte{present, e.major, e.firstEvent, e.firstError})
	case r.major == opInternAtom:
		name := string(body[4 : 4+binary.LittleEndian.Uint16(body)])
		a, ok := p.atoms[name]
		if !ok {
			p.nextAtom++
			a = p.nextAtom
			p.atoms[name] = a
		}
		p.reply(0, le32(a))
	case r.major == opGetInputFocus:
		p.reply(1, le32(0))
	case r.major == opQueryTree:
		b := make([]byte, 24+4*len(p.children))
		binary.LittleEndian.PutUint32(b[0:], peerRoot)
		binary.LittleEndian.PutUint16(b[8:], uint16(len(p.children)))
		for i, c := range p.children {
			binary.LittleEndian.PutUint32(b[24+4*i:], c)
		}
		p.reply(0, b)
	case r.major == opGetWindowAttributes:
		a := p.attrs[binary.LittleEndian.Uint32(body)]
		b := make([]byte, 36) // reply length 3
		b[18] = a[1]          // map_state is byte 26 of the reply
		b[19] = a[0]          // override_redirect, byte 27
		p.reply(0, b)
	case r.major == opGetGeometry:
		g := p.geoms[binary.LittleEndian.Uint32(body)]
		b := make([]byte, 24)
		binary.LittleEndian.PutUint32(b[0:], peerRoot)
		binary.LittleEndian.PutUint16(b[4:], uint16(g.x))
		binary.LittleEndian.PutUint16(b[6:], uint16(g.y))
		binary.LittleEndian.PutUint16(b[8:], g.w)
		binary.LittleEndian.PutUint16(b[10:], g.h)
		binary.LittleEndian.PutUint16(b[12:], g.border)
		p.reply(24, b)
	case r.major == opChangeProperty:
		win, prop := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
		format, n := body[12], int(binary.LittleEndian.Uint32(body[16:]))
		if p.props[win] == nil {
			p.props[win] = map[uint32]xpeerProp{}
		}
		p.props[win][prop] = xpeerProp{
			typ: binary.LittleEndian.Uint32(body[8:]), format: format,
			data: append([]byte(nil), body[20:20+n*int(format)/8]...),
		}
	case r.major == opGetProperty:
		win, prop := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
		v, ok := p.props[win][prop]
		b := make([]byte, 24+(len(v.data)+3)&^3)
		if ok && v.format != 0 {
			binary.LittleEndian.PutUint32(b[0:], v.typ)
			binary.LittleEndian.PutUint32(b[8:], uint32(len(v.data)/(int(v.format)/8)))
			copy(b[24:], v.data)
		}
		p.reply(v.format, b)
	case r.major == opChangeWindowAttributes:
		const cwEventMask = 1 << 11
		if p.denyRoot && binary.LittleEndian.Uint32(body) == peerRoot && binary.LittleEndian.Uint32(body[4:])&cwEventMask != 0 {
			p.errorPacket(10, opChangeWindowAttributes, peerRoot) // BadAccess
		}
	case r.major >= 128:
		switch p.extName(r.major) {
		case "BIG-REQUESTS":
			p.reply(0, le32(peerMaxWords))
		case "XC-MISC":
			if r.minor == 1 {
				p.reply(0, le32(0, 1))
			}
		case "Composite", "XFIXES":
			if r.minor == 0 {
				p.reply(0, le32(5, 4))
			}
		case "X-Resource":
			if r.minor == 0 {
				p.reply(0, []byte{1, 0, 2, 0})
			}
		}
	}
}

// requests returns a snapshot of every request received after setup.
func (p *xpeer) requests() []xpeerReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]xpeerReq(nil), p.reqs...)
}

// waitReq waits for a request satisfying match among those after the first
// skip requests, and returns it with the new count.
func (p *xpeer) waitReq(skip int, match func(xpeerReq) bool) (xpeerReq, int) {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		reqs := p.requests()
		for i := skip; i < len(reqs); i++ {
			if match(reqs[i]) {
				return reqs[i], i + 1
			}
		}
		time.Sleep(time.Millisecond)
	}
	p.t.Fatal("xpeer: expected request never arrived")
	return xpeerReq{}, 0
}

func (p *xpeer) atom(name string) uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.atoms[name]
}

func (p *xpeer) prop(win uint32, atom string) (xpeerProp, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.props[win][p.atoms[atom]]
	return v, ok
}

// event sends a 32-byte core event stamped with the current sequence number.
func (p *xpeer) event(code uint8, fill func(b *[32]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b [32]byte
	b[0] = code
	binary.LittleEndian.PutUint16(b[2:], p.seq)
	if fill != nil {
		fill(&b)
	}
	p.write(b[:])
}

func (p *xpeer) createNotify(win uint32, override bool) {
	p.event(16, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
		binary.LittleEndian.PutUint16(b[16:], 100)
		binary.LittleEndian.PutUint16(b[18:], 50)
		if override {
			b[22] = 1
		}
	})
}

func (p *xpeer) destroyNotify(win uint32) {
	p.event(17, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], peerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
	})
}

// pause makes the peer stop reading after the request it is serving.
func (p *xpeer) pause() {
	g := make(chan struct{})
	p.gate.Store(&g)
}

// resume lets the peer read again.
func (p *xpeer) resume() {
	g := *p.gate.Load()
	open := make(chan struct{})
	close(open)
	p.gate.Store(&open)
	select {
	case <-g:
	default:
		close(g)
	}
}

// set mutates the peer's script under its lock.
func (p *xpeer) set(fn func(p *xpeer)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
}

// hangup closes the server end; the client reads EOF.
func (p *xpeer) hangup() {
	if rc, err := p.f.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) { _ = unix.Shutdown(int(fd), unix.SHUT_RDWR) })
	}
}
