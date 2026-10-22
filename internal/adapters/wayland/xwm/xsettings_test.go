package xwm

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func bootSettings(t *testing.T, cfg ports.XwaylandSettings, mutate func(p *xpeer)) (*XWM, *xpeer) {
	t.Helper()
	fd, p := startXPeer(t, fullPeerExts())
	if mutate != nil {
		p.set(mutate)
	}
	x, err := BootSettings(context.Background(), fd, testLog(), cfg)
	require.NoError(t, err)
	t.Cleanup(x.CloseConn)
	return x, p
}

func (p *xpeer) count(major uint8, match func(xpeerReq) bool) (n int) {
	for _, r := range p.requests() {
		if r.major == major && (match == nil || match(r)) {
			n++
		}
	}
	return n
}

func TestXSettingsEncode(t *testing.T) {
	s := &xsettings{serial: 7, theme: "ab", size: 24, dpi: 96 * 1024, themeSerial: 7, sizeSerial: 5, dpiSerial: 3}
	n := s.encode()
	u32 := binary.LittleEndian.AppendUint32
	var want []byte
	want = append(want, 0, 0, 0, 0)
	want = u32(want, 7)
	want = u32(want, 3)
	// Gtk/CursorThemeName STRING "ab"
	want = append(want, 1, 0, 19, 0)
	want = append(want, "Gtk/CursorThemeName"...)
	want = append(want, 0) // 19 -> 20
	want = u32(want, 7)
	want = u32(want, 2)
	want = append(want, 'a', 'b', 0, 0)
	// Gtk/CursorThemeSize INT 24
	want = append(want, 0, 0, 19, 0)
	want = append(want, "Gtk/CursorThemeSize"...)
	want = append(want, 0)
	want = u32(want, 5)
	want = u32(want, 24)
	// Xft/DPI INT (name length 7 -> pad 1)
	want = append(want, 0, 0, 7, 0)
	want = append(want, "Xft/DPI"...)
	want = append(want, 0)
	want = u32(want, 3)
	want = u32(want, 96*1024)
	require.Equal(t, want, s.buf[:n])
	// Name lengths 1..4 mod 4 pad to a multiple of 4.
	for l := 1; l <= 8; l++ {
		require.Zero(t, pad4(l)%4)
		require.GreaterOrEqual(t, pad4(l), l)
		require.Less(t, pad4(l)-l, 4)
	}
}

func TestXSettingsOwnership(t *testing.T) {
	x, p := bootSettings(t, ports.XwaylandSettings{XSettings: true, CursorTheme: "Adw", CursorSize: 32}, nil)
	win := x.xs.win
	require.NotZero(t, win)
	s0, mgr := p.atom("_XSETTINGS_S0"), p.atom("MANAGER")
	own := -1
	for i, r := range p.requests() {
		if r.major == opSetSelectionOwner && r.u32(8) == s0 {
			own = i
			require.Equal(t, win, r.u32(4))
		}
	}
	require.NotEqual(t, -1, own)
	var sent bool
	for _, r := range p.requests() {
		if r.major == opSendEvent && r.u32(4) == peerRoot && r.u32(20) == mgr {
			sent = true
			require.Equal(t, s0, r.u32(28))
			require.Equal(t, win, r.u32(32))
		}
	}
	require.True(t, sent, "MANAGER client message to the root")
	prop, ok := p.prop(win, "_XSETTINGS_SETTINGS")
	require.True(t, ok)
	require.Equal(t, p.atom("_XSETTINGS_SETTINGS"), prop.typ)
	require.EqualValues(t, 8, prop.format)

	// SelectionClear stops updates.
	require.True(t, x.xs.active)
	p.event(29, func(b *[32]byte) {
		binary.LittleEndian.PutUint32(b[8:], win)
		binary.LittleEndian.PutUint32(b[12:], s0)
	})
	require.NoError(t, x.conn.Sync(&x.h))
	require.False(t, x.xs.active)
	before := p.count(opChangeProperty, nil)
	var sp Space
	sp.Set(ports.Layout{{Width: 10, Height: 10, Scale: 2}})
	x.space = &sp
	x.refreshXSettings()
	require.NoError(t, x.conn.Flush())
	require.NoError(t, x.conn.Sync(&x.h))
	require.Equal(t, before, p.count(opChangeProperty, nil))
}

func TestXSettingsExistingOwner(t *testing.T) {
	var s0 uint32
	x, p := bootSettings(t, ports.XwaylandSettings{XSettings: true}, func(p *xpeer) {
		p.selOwner = map[uint32]uint32{}
		// atoms are assigned on intern: pre-assign through a known name.
		p.nextAtom++
		s0 = p.nextAtom
		p.atoms["_XSETTINGS_S0"] = s0
		p.selOwner[s0] = 0xabc
	})
	require.False(t, x.xs.active)
	require.Zero(t, p.count(opSetSelectionOwner, func(r xpeerReq) bool { return r.u32(8) == s0 }))
	_, ok := p.prop(x.xs.win, "_XSETTINGS_SETTINGS")
	require.False(t, ok)
}

func TestXSettingsOff(t *testing.T) {
	_, p := bootSettings(t, ports.XwaylandSettings{XSettings: false, CursorSize: 24}, nil)
	s0 := p.atom("_XSETTINGS_S0")
	require.Zero(t, p.count(opSetSelectionOwner, func(r xpeerReq) bool { return r.u32(8) == s0 && s0 != 0 }))
	require.Zero(t, p.count(opGetSelectionOwner, nil))
}

// decodeSettings returns serial and the per-setting last-change serial by name.
func decodeSettings(t *testing.T, b []byte) (uint32, map[string]uint32) {
	t.Helper()
	le := binary.LittleEndian
	out := map[string]uint32{}
	n := int(le.Uint32(b[8:]))
	off := 12
	for i := 0; i < n; i++ {
		typ, nl := b[off], int(le.Uint16(b[off+2:]))
		off += 4
		name := string(b[off : off+nl])
		off += pad4(nl)
		out[name] = le.Uint32(b[off:])
		off += 4
		if typ == xsString {
			off += 4 + pad4(int(le.Uint32(b[off:])))
		} else {
			off += 4
		}
	}
	return le.Uint32(b[4:]), out
}

func TestXSettingsSerial(t *testing.T) {
	x, p := bootSettings(t, ports.XwaylandSettings{XSettings: true, CursorSize: 24}, nil)
	win := x.xs.win
	props := func() int {
		return p.count(opChangeProperty, func(r xpeerReq) bool { return r.u32(4) == win })
	}
	require.Equal(t, 1, props())
	layout := func(scale float64) {
		var sp Space
		sp.Set(ports.Layout{{Width: 100, Height: 100, Scale: scale}})
		x.space = &sp
		x.LayoutChanged(true)
		require.NoError(t, x.conn.Flush())
		require.NoError(t, x.conn.Sync(&x.h))
	}
	layout(1) // same settings as boot
	require.Equal(t, 1, props())
	layout(2)
	require.Equal(t, 2, props())
	prop, _ := p.prop(win, "_XSETTINGS_SETTINGS")
	serial, last := decodeSettings(t, prop.data)
	require.EqualValues(t, 2, serial)
	require.EqualValues(t, 2, last["Xft/DPI"])
	require.EqualValues(t, 1, last["Gtk/CursorThemeSize"])
	require.EqualValues(t, 1, last["Gtk/CursorThemeName"])
	require.EqualValues(t, 192*1024, int32(binary.LittleEndian.Uint32(prop.data[len(prop.data)-4:])))

	x.xs.serial = 0xffffffff
	x.xs.dpi = 0
	x.publishXSettings(false)
	require.EqualValues(t, 0, x.xs.serial, "wraps")
}

func TestXSettingsNoChangeAllocations(t *testing.T) {
	x, _ := bootSettings(t, ports.XwaylandSettings{XSettings: true, CursorTheme: "Adw", CursorSize: 24}, nil)
	var sp Space
	sp.Set(ports.Layout{{Width: 100, Height: 100, Scale: 1}})
	x.space = &sp
	x.LayoutChanged(true)
	l := ports.Layout{{Width: 100, Height: 100, Scale: 1}}
	n := testing.AllocsPerRun(100, func() {
		sp.Set(l)
		x.LayoutChanged(false)
	})
	require.Zero(t, n)
}

// The XSETTINGS manager window is never adopted as a client window.
func TestXSettingsWindowNotAdopted(t *testing.T) {
	x, p := bootSettings(t, ports.XwaylandSettings{XSettings: true}, nil)
	h := newMockhost(t)
	post, _ := newPost()
	x.attach(newMockfdSource(t), h, post)
	require.NotZero(t, x.xs.win)
	p.createNotify(x.xs.win, false)
	ev := x.Stats().Events
	readUntil(t, x, func() bool { return x.Stats().Events > ev })
	x.flush()
	require.Zero(t, x.tab.lookup(x.xs.win))
}
