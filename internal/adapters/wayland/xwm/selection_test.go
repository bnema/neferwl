package xwm

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
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
