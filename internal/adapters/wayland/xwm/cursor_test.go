package xwm

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func rootCursorExts() map[string]xpeerExt {
	e := fullPeerExts()
	e["RENDER"] = xpeerExt{139, 0, 0}
	return e
}

func bootCursor(t *testing.T, exts map[string]xpeerExt, mutate func(*xpeer), cfg ports.XwaylandSettings) (*XWM, *xpeer) {
	t.Helper()
	fd, p := startXPeer(t, exts)
	if mutate != nil {
		p.set(mutate)
	}
	x, err := BootSettings(t.Context(), fd, testLog(), cfg)
	require.NoError(t, err)
	t.Cleanup(x.CloseConn)
	return x, p
}

func cursorCfg() ports.XwaylandSettings {
	return ports.XwaylandSettings{RootCursor: true, Cursor: ports.CursorImage{W: 2, H: 2, HotX: 1, HotY: 0, Pixels: make([]byte, 16)}}
}

func TestRootCursorRequests(t *testing.T) {
	_, p := bootCursor(t, rootCursorExts(), nil, cursorCfg())
	// QueryPictFormats (render minor 1) comes first; then the core requests.
	var got []string
	for _, r := range p.requests() {
		switch {
		case r.major == 139 && r.minor == 1:
			got = append(got, "QueryPictFormats")
		case r.major == 53:
			got = append(got, "CreatePixmap")
		case r.major == 72:
			got = append(got, "PutImage")
		case r.major == 139 && r.minor == 4:
			got = append(got, "CreatePicture")
		case r.major == 139 && r.minor == 27:
			got = append(got, "CreateCursor")
		case r.major == opChangeWindowAttributes && r.u32(4) == peerRoot && r.u32(8)&(1<<14) != 0:
			got = append(got, "ChangeWindowAttributes(root)")
		case r.major == 54:
			got = append(got, "FreePixmap")
		}
	}
	require.Equal(t, []string{"QueryPictFormats", "CreatePixmap", "PutImage", "CreatePicture", "CreateCursor",
		"ChangeWindowAttributes(root)", "FreePixmap"}, got)
	for _, r := range p.requests() {
		if r.major == 72 { // PutImage: ZPixmap, depth 32
			require.EqualValues(t, 2, r.raw[1])
			require.EqualValues(t, 32, r.raw[21])
		}
	}
}

func TestRootCursorFallbackWithoutRender(t *testing.T) {
	_, p := bootCursor(t, fullPeerExts(), nil, cursorCfg())
	var open, glyph, set bool
	for _, r := range p.requests() {
		open = open || r.major == 45
		glyph = glyph || r.major == 94
		set = set || r.major == opChangeWindowAttributes && r.u32(4) == peerRoot && r.u32(8)&(1<<14) != 0
	}
	require.True(t, open && glyph && set, "OpenFont, CreateGlyphCursor, root cursor")
	require.Zero(t, p.count(53, nil), "no pixmap")
}

func TestRootCursorFallbackWithoutARGB(t *testing.T) {
	_, p := bootCursor(t, rootCursorExts(), func(p *xpeer) { p.noARGB = true }, cursorCfg())
	require.NotZero(t, p.count(94, nil))
	require.Zero(t, p.count(72, nil))
}

func TestRootCursorOff(t *testing.T) {
	_, p := bootCursor(t, rootCursorExts(), nil, ports.XwaylandSettings{})
	require.Zero(t, p.count(94, nil))
	require.Zero(t, p.count(53, nil))
}
