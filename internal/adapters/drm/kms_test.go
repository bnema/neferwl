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
	mode := func(w, h uint16, hz uint32, pref bool) modeInfo {
		// htotal*vtotal = w*h keeps refreshMilli = clock*1000/(w*h).
		m := modeInfo{HDisplay: w, VDisplay: h, HTotal: w, VTotal: h, Clock: uint32(uint64(w) * uint64(h) * uint64(hz) / 1000), VRefresh: hz}
		if pref {
			m.Type = modeTypePrefered
		}
		return m
	}
	lg := connector{name: "DP-2", connected: true, modes: []modeInfo{mode(5120, 2160, 165, true), mode(3440, 1440, 100, true), mode(5120, 2160, 100, false), mode(5120, 2160, 60, false)}}
	tv := connector{name: "HDMI-A-1", connected: true, modes: []modeInfo{mode(3840, 2160, 60, true), mode(3840, 2160, 120, false)}}
	off := connector{name: "DP-1", modes: []modeInfo{mode(1920, 1080, 60, true)}}
	for _, tc := range []struct {
		name  string
		conns []connector
		want  Want
		conn  string
		mode  string
		err   bool
	}{
		{"none", nil, Want{}, "", "", true},
		{"first connected, first preferred", []connector{off, tv, lg}, Want{}, "HDMI-A-1", "3840x2160@60.000", false},
		{"named", []connector{tv, lg}, Want{Name: "DP-2"}, "DP-2", "5120x2160@165.000", false},
		{"named mode closest refresh", []connector{tv, lg}, Want{Name: "DP-2", W: 5120, H: 2160, Hz: 99}, "DP-2", "5120x2160@100.000", false},
		{"named mode highest refresh", []connector{lg}, Want{Name: "DP-2", W: 5120, H: 2160}, "DP-2", "5120x2160@165.000", false},
		{"unknown mode falls back to preferred", []connector{lg}, Want{Name: "DP-2", W: 800, H: 600}, "DP-2", "5120x2160@165.000", false},
		{"missing name falls back", []connector{tv}, Want{Name: "DP-2"}, "HDMI-A-1", "3840x2160@60.000", false},
		{"disabled skipped", []connector{tv, lg}, Want{Disabled: map[string]bool{"HDMI-A-1": true}}, "DP-2", "5120x2160@165.000", false},
		{"strict finds named", []connector{tv, lg}, Want{Name: "DP-2", Strict: true}, "DP-2", "5120x2160@165.000", false},
		{"strict without named", []connector{tv}, Want{Name: "DP-2", Strict: true}, "", "", true},
		{"all disabled", []connector{tv}, Want{Disabled: map[string]bool{"HDMI-A-1": true}}, "", "", true},
	} {
		c, m, err := pickConnector(tc.conns, tc.want)
		if (err != nil) != tc.err || c.name != tc.conn || (err == nil && m.String() != tc.mode) {
			t.Errorf("%s: got %q %s %v", tc.name, c.name, m, err)
		}
	}
}

func TestRefreshMilliLG(t *testing.T) {
	// Real 5120x2160@165.058 CVT-RB timings.
	m := modeInfo{Clock: 1_989_530, HDisplay: 5120, HTotal: 5200, VDisplay: 2160, VTotal: 2318}
	if got := m.refreshMilli(); got < 165_000 || got > 165_100 {
		t.Fatalf("refresh %d mHz", got)
	}
}

func TestParseEDID(t *testing.T) {
	d := make([]byte, 128)
	copy(d, "\x00\xff\xff\xff\xff\xff\xff\x00")
	d[8], d[9] = 0x1e, 0x6d // GSM
	copy(d[54:], []byte{0, 0, 0, 0xfc, 0})
	copy(d[59:], "LG ULTRAGEAR+\n")
	copy(d[72:], []byte{0, 0, 0, 0xff, 0})
	copy(d[77:], "SN123\n       ")
	m := parseEDID(d)
	if m.Model != "LG ULTRAGEAR+" || m.Serial != "SN123" || (m.Make != "GSM" && m.Make != "LG Electronics") {
		t.Fatalf("%+v", m)
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
