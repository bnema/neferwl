package drm

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestStructSizesMatchIoctls(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uintptr
		req  uintptr
	}{
		{"card_res", unsafe.Sizeof(cardRes{}), ioctlGetResources},
		{"mode_crtc", unsafe.Sizeof(modeCrtc{}), ioctlSetCrtc},
		{"get_encoder", unsafe.Sizeof(getEncoder{}), ioctlGetEncoder},
		{"get_connector", unsafe.Sizeof(getConnector{}), ioctlGetConnector},
		{"fb_cmd", unsafe.Sizeof(fbCmd{}), ioctlAddFB},
		{"page_flip", unsafe.Sizeof(pageFlip{}), ioctlPageFlip},
		{"create_dumb", unsafe.Sizeof(createDumb{}), ioctlCreateDumb},
		{"map_dumb", unsafe.Sizeof(mapDumb{}), ioctlMapDumb},
	} {
		if want := (tc.req >> 16) & 0x3fff; tc.got != want {
			t.Errorf("%s: size %d, ioctl encodes %d", tc.name, tc.got, want)
		}
	}
	if unsafe.Sizeof(modeInfo{}) != 68 {
		t.Errorf("modeInfo size %d, want 68", unsafe.Sizeof(modeInfo{}))
	}
}

func TestPickConnector(t *testing.T) {
	plain := modeInfo{HDisplay: 1280, VDisplay: 720}
	pref := modeInfo{HDisplay: 2560, VDisplay: 1440, Type: modeTypePrefered}
	for _, tc := range []struct {
		name  string
		conns []connector
		want  string
		mode  uint16
		err   bool
	}{
		{"none", nil, "", 0, true},
		{"disconnected skipped", []connector{{name: "DP-1", modes: []modeInfo{pref}}, {name: "DP-2", connected: true, modes: []modeInfo{plain}}}, "DP-2", 1280, false},
		{"preferred mode", []connector{{name: "DP-1", connected: true, modes: []modeInfo{plain, pref}}}, "DP-1", 2560, false},
		{"no modes skipped", []connector{{name: "DP-1", connected: true}}, "", 0, true},
	} {
		c, m, err := pickConnector(tc.conns)
		if (err != nil) != tc.err || c.name != tc.want || m.HDisplay != tc.mode {
			t.Errorf("%s: got %q %d %v", tc.name, c.name, m.HDisplay, err)
		}
	}
}

func TestCountFlipEvents(t *testing.T) {
	ev := func(typ uint32, n int) []byte {
		b := make([]byte, n)
		binary.LittleEndian.PutUint32(b, typ)
		binary.LittleEndian.PutUint32(b[4:], uint32(n))
		return b
	}
	buf := append(append(ev(eventFlipDone, 32), ev(1, 32)...), ev(eventFlipDone, 32)...)
	if n := countFlipEvents(buf); n != 2 {
		t.Fatalf("got %d flips", n)
	}
	if n := countFlipEvents(buf[:20]); n != 0 {
		t.Fatalf("truncated: got %d", n)
	}
}
