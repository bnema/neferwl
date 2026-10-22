package wayland

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// x11Peer is a scripted X server speaking the real wire protocol over a
// socketpair: setup, extensions, atoms, properties, geometry. It records every
// request. It is a protocol peer, not a double of a project interface.
type x11Peer struct {
	t  *testing.T
	f  *os.File
	mu sync.Mutex

	seq      uint16
	reqs     []x11Req
	atoms    map[string]uint32
	nextAtom uint32
	props    map[uint32]map[uint32][]byte // window -> atom -> typed data (type, format, data)
	propMeta map[uint32]map[uint32][2]uint32
	done     chan struct{}
}

type x11Req struct {
	major, minor uint8
	raw          []byte
}

func (r x11Req) u32(off int) uint32 { return binary.LittleEndian.Uint32(r.raw[off:]) }

const (
	x11PeerRoot = 0x100
	x11Win      = 0x500001
	// Opcodes the tests look for.
	x11OpConfigureWindow = 12
	x11OpSetInputFocus   = 42
	x11OpKillClient      = 113
)

func startX11Peer(t *testing.T) (int, *x11Peer) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &x11Peer{
		t: t, f: os.NewFile(uintptr(fds[1]), "x11peer"), atoms: map[string]uint32{}, nextAtom: 1000,
		props: map[uint32]map[uint32][]byte{}, propMeta: map[uint32]map[uint32][2]uint32{}, done: make(chan struct{}),
	}
	t.Cleanup(func() { p.f.Close(); <-p.done })
	go p.serve()
	return fds[0], p
}

func (p *x11Peer) setupReply() []byte {
	vendor := []byte("x11pr\x00\x00\x00")
	b := make([]byte, 40+len(vendor)+40)
	b[0], b[2] = 1, 11
	binary.LittleEndian.PutUint16(b[6:], uint16((len(b)-8)/4))
	binary.LittleEndian.PutUint32(b[12:], 0x200000)
	binary.LittleEndian.PutUint32(b[16:], 0x1fffff)
	binary.LittleEndian.PutUint16(b[24:], 5)
	b[28] = 1
	copy(b[40:], vendor)
	scr := b[40+len(vendor):]
	binary.LittleEndian.PutUint32(scr, x11PeerRoot)
	binary.LittleEndian.PutUint32(scr[32:], 0x22)
	scr[38] = 24
	return b
}

func (p *x11Peer) serve() {
	defer close(p.done)
	var hdr [12]byte
	if _, err := io.ReadFull(p.f, hdr[:]); err != nil {
		return
	}
	if _, err := p.f.Write(p.setupReply()); err != nil {
		return
	}
	for {
		var h [4]byte
		if _, err := io.ReadFull(p.f, h[:]); err != nil {
			return
		}
		raw := h[:]
		n := int(binary.LittleEndian.Uint16(h[2:])) * 4
		if n == 0 {
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
		r := x11Req{h[0], h[1], raw}
		p.reqs = append(p.reqs, r)
		p.answer(r, raw[len(raw)-len(rest):])
		p.mu.Unlock()
	}
}

func (p *x11Peer) reply(data uint8, body []byte) {
	words := (max(len(body), 24) - 24 + 3) / 4
	b := make([]byte, 32+4*words)
	b[0], b[1] = 1, data
	binary.LittleEndian.PutUint16(b[2:], p.seq)
	binary.LittleEndian.PutUint32(b[4:], uint32(words))
	copy(b[8:], body)
	_, _ = p.f.Write(b)
}

func x11le32(v ...uint32) []byte {
	b := make([]byte, 0, 4*len(v))
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, x)
	}
	return b
}

var x11Exts = map[string]uint8{"BIG-REQUESTS": 133, "XC-MISC": 136, "Composite": 142}

func (p *x11Peer) answer(r x11Req, body []byte) {
	switch {
	case r.major == 98: // QueryExtension
		major, ok := x11Exts[string(body[4:4+binary.LittleEndian.Uint16(body)])]
		present := uint8(0)
		if ok {
			present = 1
		}
		p.reply(0, []byte{present, major, 0, 0})
	case r.major == 16: // InternAtom
		name := string(body[4 : 4+binary.LittleEndian.Uint16(body)])
		a, ok := p.atoms[name]
		if !ok {
			p.nextAtom++
			a = p.nextAtom
			p.atoms[name] = a
		}
		p.reply(0, x11le32(a))
	case r.major == 43: // GetInputFocus (Sync)
		p.reply(1, x11le32(0))
	case r.major == 15: // QueryTree
		b := make([]byte, 24)
		binary.LittleEndian.PutUint32(b, x11PeerRoot)
		p.reply(0, b)
	case r.major == 14: // GetGeometry
		b := make([]byte, 24)
		binary.LittleEndian.PutUint32(b, x11PeerRoot)
		binary.LittleEndian.PutUint16(b[8:], 300)
		binary.LittleEndian.PutUint16(b[10:], 200)
		p.reply(24, b)
	case r.major == 18: // ChangeProperty
		win, prop := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
		format, n := body[12], int(binary.LittleEndian.Uint32(body[16:]))
		p.store(win, prop, binary.LittleEndian.Uint32(body[8:]), uint32(format), body[20:20+n*int(format)/8])
	case r.major == 20: // GetProperty
		win, prop := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
		data, ok := p.props[win][prop]
		b := make([]byte, 24+(len(data)+3)&^3)
		var format uint8
		if ok {
			meta := p.propMeta[win][prop]
			format = uint8(meta[1])
			binary.LittleEndian.PutUint32(b[0:], meta[0])
			binary.LittleEndian.PutUint32(b[8:], uint32(len(data)/(int(format)/8)))
			copy(b[24:], data)
		}
		p.reply(format, b)
	case r.major >= 128:
		switch r.major {
		case 133:
			p.reply(0, x11le32(0x100000))
		case 136:
			if r.minor == 1 {
				p.reply(0, x11le32(0, 1))
			}
		case 142:
			if r.minor == 0 {
				p.reply(0, x11le32(0, 4))
			}
		}
	}
}

func (p *x11Peer) store(win, prop, typ, format uint32, data []byte) {
	if p.props[win] == nil {
		p.props[win], p.propMeta[win] = map[uint32][]byte{}, map[uint32][2]uint32{}
	}
	p.props[win][prop] = append([]byte(nil), data...)
	p.propMeta[win][prop] = [2]uint32{typ, format}
}

// atom interns name the way the XWM would have.
func (p *x11Peer) atom(name string) uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.atoms[name]
	if !ok {
		p.nextAtom++
		a = p.nextAtom
		p.atoms[name] = a
	}
	return a
}

// setProp scripts a window property (typ is an atom number, format 8 or 32).
func (p *x11Peer) setProp(win, prop, typ, format uint32, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store(win, prop, typ, format, data)
}

func (p *x11Peer) event(code uint8, fill func(b *[32]byte)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b [32]byte
	b[0] = code
	binary.LittleEndian.PutUint16(b[2:], p.seq)
	fill(&b)
	_, _ = p.f.Write(b[:])
}

func (p *x11Peer) createNotify(win uint32) {
	p.event(16, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], x11PeerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
		binary.LittleEndian.PutUint16(b[16:], 100)
		binary.LittleEndian.PutUint16(b[18:], 50)
	})
}

func (p *x11Peer) windowEvent(code uint8, win uint32) {
	p.event(code, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], x11PeerRoot)
		binary.LittleEndian.PutUint32(b[8:], win)
	})
}

func (p *x11Peer) mapRequest(win uint32)    { p.windowEvent(20, win) }
func (p *x11Peer) unmapNotify(win uint32)   { p.windowEvent(18, win) }
func (p *x11Peer) destroyNotify(win uint32) { p.windowEvent(17, win) }

func (p *x11Peer) propertyNotify(win, atom uint32) {
	p.event(28, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[4:], win)
		binary.LittleEndian.PutUint32(b[8:], atom)
	})
}

func (p *x11Peer) clientMessage(win uint32, typ string, data ...uint32) {
	t := p.atom(typ)
	p.event(33, func(b *[32]byte) {
		b[1] = 32
		binary.LittleEndian.PutUint32(b[4:], win)
		binary.LittleEndian.PutUint32(b[8:], t)
		for i, d := range data {
			binary.LittleEndian.PutUint32(b[12+4*i:], d)
		}
	})
}

func (p *x11Peer) requests() []x11Req {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]x11Req(nil), p.reqs...)
}

// waitReq waits for a request after the first skip ones that satisfies match.
func (p *x11Peer) waitReq(skip int, match func(x11Req) bool) (x11Req, int) {
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
	p.t.Fatal("x11 peer: expected request never arrived")
	return x11Req{}, 0
}
