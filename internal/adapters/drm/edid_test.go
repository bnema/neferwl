package drm

import (
	"math"
	"testing"
)

func TestEDIDHDR(t *testing.T) {
	d := make([]byte, 256)
	copy(d, "\x00\xff\xff\xff\xff\xff\xff\x00")
	d[20] = 0xb0 // digital, 10 bits per color
	d[25], d[26] = 0b_10_01_11_00, 0b_01_11_10_00
	d[27], d[28] = 128, 64
	d[29], d[30] = 32, 16
	d[31], d[32] = 8, 4
	d[33], d[34] = 2, 1
	d[126] = 1
	d[128], d[130] = 2, 15
	// CTA extended colorimetry: BT2020RGB + BT2020YCC.
	copy(d[132:], []byte{0xe3, 5, 0xc0, 0})
	// EOTF SDR, PQ, HLG; static type 1; max 200, average 100, min 200/100.
	copy(d[136:], []byte{0xe6, 6, 0x0d, 1, 64, 32, 255})
	m := parseEDID(d)
	if m.BitsPerColor != 10 || !m.HDR.SDR || !m.HDR.PQ || !m.HDR.HLG || !m.HDR.StaticType1 || !m.HDR.BT2020RGB || !m.HDR.BT2020YCC {
		t.Fatalf("%+v", m)
	}
	if m.Chromaticity.RedX != float64(128*4+2)/1024 || m.Chromaticity.GreenY != float64(16*4)/1024 || m.Chromaticity.WhiteY != float64(1*4)/1024 {
		t.Fatalf("chromaticity: %+v", m.Chromaticity)
	}
	if math.Abs(m.HDR.MaxLuminance-200) > 1e-9 || math.Abs(m.HDR.MaxFrameAverage-100) > 1e-9 || math.Abs(m.HDR.MinLuminance-2) > 1e-9 {
		t.Fatalf("luminances: %+v", m.HDR)
	}
	for _, n := range []int{0, 7, 127, 128, 129, 255} {
		got := parseEDID(d[:n])
		if got.HDR.PQ {
			t.Fatalf("truncated %d: %+v", n, got)
		}
	}
	d[132] = 0xff // block exceeds DTD boundary
	if parseEDID(d).HDR.PQ {
		t.Fatal("parsed malformed block")
	}
	d[132] = 0xe3
	d[130] = 3 // invalid CTA offset
	if parseEDID(d).HDR.PQ {
		t.Fatal("parsed invalid CTA offset")
	}
	d[130] = 15
	d[136] = 0xe2 // HDR payload without mandatory descriptor byte
	if parseEDID(d).HDR.PQ {
		t.Fatal("parsed truncated HDR payload")
	}
	d[20] = 0 // analog input cannot specify digital bit depth
	if parseEDID(d).BitsPerColor != 0 {
		t.Fatal("analog input has bit depth")
	}
}

func TestDetectHDR(t *testing.T) {
	m := Monitor{HDR: HDRMetadata{PQ: true, BT2020RGB: true}}
	p := connectorHDRProps{Metadata: 1, Colorspace: 2, MaxBPC: 3, HasDefault: true}
	cases := []struct {
		name   string
		m      Monitor
		p      connectorHDRProps
		reason string
	}{
		{"ready", m, p, ""},
		{"no PQ", Monitor{HDR: HDRMetadata{BT2020RGB: true}}, p, "edid: no PQ"},
		{"no RGB", Monitor{HDR: HDRMetadata{PQ: true}}, p, "edid: no BT2020RGB"},
		{"no metadata", m, connectorHDRProps{Colorspace: 2, MaxBPC: 3, HasDefault: true}, "connector: no HDR_OUTPUT_METADATA"},
		{"no colorspace", m, connectorHDRProps{Metadata: 1, MaxBPC: 3}, "connector: no BT2020_RGB Colorspace"},
		{"no bpc", m, connectorHDRProps{Metadata: 1, Colorspace: 2, HasDefault: true}, "connector: max bpc below 10"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := detectHDR(tt.m, tt.p)
			if c.Capable != (tt.reason == "") || c.Reason != tt.reason {
				t.Fatalf("%+v", c)
			}
		})
	}
}
