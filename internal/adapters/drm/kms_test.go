package drm

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
)

func TestStructSizesMatchIoctls(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uintptr
		req  uintptr
	}{
		{"card_res", unsafe.Sizeof(cardRes{}), ioctlGetResources},
		{"get_encoder", unsafe.Sizeof(getEncoder{}), ioctlGetEncoder},
		{"get_connector", unsafe.Sizeof(getConnector{}), ioctlGetConnector},
		{"fb_cmd2", unsafe.Sizeof(fbCmd2{}), ioctlAddFB2},
		{"prime_handle", unsafe.Sizeof(primeHandle{}), ioctlPrimeFDToHandle},
		{"gem_close", unsafe.Sizeof(gemClose{}), ioctlGemClose},
		{"obj_get_props", unsafe.Sizeof(objGetProps{}), ioctlObjGetProps},
		{"get_prop", unsafe.Sizeof(getProp{}), ioctlGetProp},
		{"mode_crtc", unsafe.Sizeof(modeCrtc{}), ioctlGetCrtc},
		{"set_client_cap", unsafe.Sizeof(setClientCap{}), ioctlSetClientCap},
		{"get_plane_res", unsafe.Sizeof(getPlaneRes{}), ioctlGetPlaneRes},
		{"get_plane", unsafe.Sizeof(getPlane{}), ioctlGetPlane},
		{"get_blob", unsafe.Sizeof(getBlob{}), ioctlGetPropBlob},
		{"atomic", unsafe.Sizeof(modeAtomic{}), ioctlAtomic},
		{"create_blob", unsafe.Sizeof(createBlob{}), ioctlCreateBlob},
		{"get_cap", unsafe.Sizeof(getCap{}), ioctlGetCap},
		{"destroy_blob", unsafe.Sizeof(uint32(0)), ioctlDestroyBlob},
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

func TestParseFlips(t *testing.T) {
	ev := func(typ, crtc uint32, user uint64, sec, usec, seq uint32) []byte {
		b := make([]byte, 32)
		le := binary.LittleEndian
		le.PutUint32(b, typ)
		le.PutUint32(b[4:], 32)
		le.PutUint64(b[8:], user)
		le.PutUint32(b[16:], sec)
		le.PutUint32(b[20:], usec)
		le.PutUint32(b[24:], seq)
		le.PutUint32(b[28:], crtc)
		return b
	}
	buf := append(append(ev(eventFlipDone, 41, userFrame, 3, 250, 7), ev(1, 7, 0, 0, 0, 0)...), ev(eventFlipDone, 42, userState, 0, 1, 8)...)
	got := parseFlips(buf)
	want := []flipEvent{{crtc: 41, user: userFrame, when: 3*time.Second + 250*time.Microsecond, seq: 7}, {crtc: 42, user: userState, when: time.Microsecond, seq: 8}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := parseFlips(buf[:20]); len(got) != 0 {
		t.Fatalf("truncated: got %v", got)
	}
}

func TestParseInFormats(t *testing.T) {
	le := binary.LittleEndian
	// 3 formats at 24, 2 modifiers at 40.
	b := make([]byte, 40+2*24)
	le.PutUint32(b, 1)
	le.PutUint32(b[8:], 3)
	le.PutUint32(b[12:], 24)
	le.PutUint32(b[16:], 2)
	le.PutUint32(b[20:], 40)
	for i, f := range []uint32{fourccXRGB, fourccARGB, 0x3432564e} {
		le.PutUint32(b[24+i*4:], f)
	}
	// linear on formats 0 and 2, a tiled modifier on format 1.
	le.PutUint64(b[40:], 0b101)
	le.PutUint64(b[56:], 0)
	le.PutUint64(b[64:], 0b010)
	le.PutUint64(b[80:], 0x0200000000000001)
	got := parseInFormats(b)
	want := []ports.DMABufFormat{{Format: fourccXRGB}, {Format: 0x3432564e}, {Format: fourccARGB, Modifier: 0x0200000000000001}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if parseInFormats(b[:30]) != nil {
		t.Fatal("truncated blob parsed")
	}
}
