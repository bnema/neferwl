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
		{"fb_cmd2", unsafe.Sizeof(fbCmd2{}), ioctlAddFB2},
		{"prime_handle", unsafe.Sizeof(primeHandle{}), ioctlPrimeFDToHandle},
		{"gem_close", unsafe.Sizeof(gemClose{}), ioctlGemClose},
	} {
		if want := (tc.req >> 16) & 0x3fff; tc.got != want {
			t.Errorf("%s: size %d, ioctl encodes %d", tc.name, tc.got, want)
		}
	}
	if unsafe.Sizeof(modeInfo{}) != 68 {
		t.Errorf("modeInfo size %d, want 68", unsafe.Sizeof(modeInfo{}))
	}
}

func TestWantPicksMode(t *testing.T) {
	mode := func(w, h uint16, hz uint32, pref bool) modeInfo {
		// htotal*vtotal = w*h keeps refreshMilli = clock*1000/(w*h).
		m := modeInfo{HDisplay: w, VDisplay: h, HTotal: w, VTotal: h, Clock: uint32(uint64(w) * uint64(h) * uint64(hz) / 1000), VRefresh: hz}
		if pref {
			m.Type = modeTypePrefered
		}
		return m
	}
	lg := connector{name: "DP-2", connected: true, modes: []modeInfo{mode(5120, 2160, 165, true), mode(3440, 1440, 100, true), mode(5120, 2160, 100, false), mode(5120, 2160, 60, false)}}
	off := connector{name: "DP-1", modes: []modeInfo{mode(1920, 1080, 60, true)}}
	for _, tc := range []struct {
		name string
		want Want
		mode string
	}{
		{"first preferred", Want{}, "5120x2160@165.000"},
		{"closest refresh", Want{Modes: map[string][3]float64{"DP-2": {5120, 2160, 99}}}, "5120x2160@100.000"},
		{"highest refresh", Want{Modes: map[string][3]float64{"DP-2": {5120, 2160, 0}}}, "5120x2160@165.000"},
		{"unknown mode falls back to preferred", Want{Modes: map[string][3]float64{"DP-2": {800, 600, 0}}}, "5120x2160@165.000"},
	} {
		if m := tc.want.pickMode(lg); m.String() != tc.mode {
			t.Errorf("%s: got %s", tc.name, m)
		}
	}
	if (Want{}).usable(off) || !(Want{}).usable(lg) || (Want{Disabled: map[string]bool{"DP-2": true}}).usable(lg) {
		t.Fatal("usable")
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

func TestFlipCrtcs(t *testing.T) {
	ev := func(typ, crtc uint32) []byte {
		b := make([]byte, 32)
		binary.LittleEndian.PutUint32(b, typ)
		binary.LittleEndian.PutUint32(b[4:], 32)
		binary.LittleEndian.PutUint32(b[28:], crtc)
		return b
	}
	buf := append(append(ev(eventFlipDone, 41), ev(1, 7)...), ev(eventFlipDone, 42)...)
	if got := flipCrtcs(buf); len(got) != 2 || got[0] != 41 || got[1] != 42 {
		t.Fatalf("got %v", got)
	}
	if got := flipCrtcs(buf[:20]); len(got) != 0 {
		t.Fatalf("truncated: got %v", got)
	}
}
