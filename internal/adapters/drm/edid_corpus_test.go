package drm

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestEDIDCorpus(t *testing.T) {
	props := connectorHDRProps{Metadata: 1, Colorspace: 2, MaxBPC: 3, BT2020Value: 9, HasDefault: true, MaxBPCValue: 8}
	for _, tt := range []struct {
		name, file    string
		data          []byte
		tier          domain.HDRTier
		source        domain.HDRPeakSource
		peak, average float64
		expandable    bool
	}{
		{name: "LG UltraGear+", file: "lg-ultragear-plus.bin", tier: domain.HDRFull, source: domain.HDRPeakEDID, peak: 603.665771037369, average: 276.78197639276635, expandable: true},
		{name: "Gigabyte M32U", file: "gigabyte-m32u.bin", tier: domain.HDRFull, source: domain.HDRPeakEDID, peak: 445.754697038357, average: 445.754697038357, expandable: true},
		{name: "PQ unspecified peak", data: capabilityEDID(true, 4, 0), tier: domain.HDRAssumed, source: domain.HDRPeakDefault, peak: 400, expandable: true},
		{name: "SDR without HDR block", data: capabilityEDID(false, 0, 0), tier: domain.HDRUnavailable, source: domain.HDRPeakDefault},
		{name: "HLG only", data: capabilityEDID(true, 8, 115), tier: domain.HDRUnavailable, source: domain.HDRPeakDefault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.data
			if tt.file != "" {
				var err error
				data, err = os.ReadFile("testdata/edid/" + tt.file)
				if err != nil {
					t.Fatal(err)
				}
			}
			m := parseEDID(data)
			t.Logf("model=%s HDR=%+v", m.Model, m.HDR)
			if tt.file != "" && math.Abs(m.HDR.MaxFrameAverage-tt.average) > 1 {
				t.Fatalf("frame-average = %g, want %g", m.HDR.MaxFrameAverage, tt.average)
			}
			caps := detectHDR(m, props).caps(0, 203)
			if caps.Tier != tt.tier || caps.PeakSource != tt.source || math.Abs(caps.PeakNits-tt.peak) > 1 || math.Abs(caps.Headroom-tt.peak/203) > 0.005 || caps.Expandable() != tt.expandable {
				t.Fatalf("caps = %+v (expandable %t), want tier=%s source=%s peak=%g headroom=%g expandable=%t", caps, caps.Expandable(), tt.tier, tt.source, tt.peak, tt.peak/203, tt.expandable)
			}
		})
	}
}

// capabilityEDID builds a base block and CTA-861 extension with valid checksums.
// Colorimetry and static type 1 are present so EOTF/peak alone control the cases.
func capabilityEDID(hdr bool, eotf, peak byte) []byte {
	d := make([]byte, 256)
	copy(d, "\x00\xff\xff\xff\xff\xff\xff\x00")
	d[18], d[19], d[20], d[126] = 1, 4, 0xb0, 1
	cta := d[128:]
	cta[0], cta[1], cta[2] = 2, 3, 8
	copy(cta[4:], []byte{0xe3, 5, 0xc0, 0}) // BT.2020 RGB and YCC
	if hdr {
		copy(cta[8:], []byte{0xe6, 6, eotf, 1, peak, 0, 0})
		cta[2] = 15
	}
	for _, block := range [][]byte{d[:128], cta} {
		var sum byte
		for _, b := range block[:127] {
			sum += b
		}
		block[127] = -sum
	}
	return d
}

// Scan every byte, not only base-block descriptor slots: extensions may also
// contain serial descriptors. Metadata changes must preserve block checksums.
func TestEDIDCorpusHasNoSerials(t *testing.T) {
	files, err := filepath.Glob("testdata/edid/*.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no EDID fixtures")
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) < 128 || len(data)%128 != 0 {
				t.Fatalf("invalid EDID length %d", len(data))
			}
			serial := binary.LittleEndian.Uint32(data[12:16])
			if serial != 0 && serial != 0x01010101 {
				t.Error("fixture contains a numeric serial")
			}
			for offset := 0; offset+18 <= len(data); offset++ {
				if !bytes.Equal(data[offset:offset+5], []byte{0, 0, 0, 0xff, 0}) {
					continue
				}
				text := bytes.Trim(data[offset+5:offset+18], " \n\r\x00")
				if len(bytes.Trim(text, "0")) != 0 {
					t.Errorf("fixture contains a non-generic serial descriptor at offset %d", offset)
				}
			}
			for offset := 0; offset < len(data); offset += 128 {
				var sum byte
				for _, b := range data[offset : offset+128] {
					sum += b
				}
				if sum != 0 {
					t.Errorf("invalid checksum in block %d", offset/128)
				}
			}
		})
	}
}
