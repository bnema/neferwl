package drm

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Monitor identifies the display attached to a connector.
type Monitor struct {
	Make, Model, Serial string
	Chromaticity        Chromaticity
	BitsPerColor        int // 0 when unspecified
	HDR                 HDRMetadata
}

// Chromaticity is the EDID base block's 10-bit CIE xy coordinates.
type Chromaticity struct {
	RedX, RedY, GreenX, GreenY, BlueX, BlueY, WhiteX, WhiteY float64
}

// HDRMetadata is the CTA-861 HDR static metadata and BT.2020 colorimetry.
type HDRMetadata struct {
	SDR, PQ, HLG, StaticType1, BT2020RGB, BT2020YCC bool
	MaxLuminance, MaxFrameAverage, MinLuminance     float64 // cd/m²; zero when unspecified
}

// readMonitor parses the connector's EDID from sysfs. Missing data yields "Unknown".
func readMonitor(card, connector string) Monitor {
	data, err := os.ReadFile(filepath.Join("/sys/class/drm", filepath.Base(card)+"-"+connector, "edid"))
	if err != nil {
		return Monitor{Make: "Unknown", Model: "Unknown"}
	}
	return parseEDID(data)
}

func parseEDID(d []byte) Monitor {
	m := Monitor{Make: "Unknown", Model: "Unknown"}
	if len(d) < 128 || string(d[:8]) != "\x00\xff\xff\xff\xff\xff\xff\x00" {
		return m
	}
	id := uint16(d[8])<<8 | uint16(d[9])
	pnp := string([]byte{byte(id>>10&0x1f) + '@', byte(id>>5&0x1f) + '@', byte(id&0x1f) + '@'})
	m.Make = pnpName(pnp)
	if d[20]&0x80 != 0 {
		m.BitsPerColor = []int{0, 6, 8, 10, 12, 14, 16, 0}[d[20]>>4&7]
	}
	xy := func(hi, lo byte) float64 { return float64(uint16(hi)<<2|uint16(lo)) / 1024 }
	m.Chromaticity = Chromaticity{
		RedX: xy(d[27], d[25]>>6), RedY: xy(d[28], d[25]>>4&3),
		GreenX: xy(d[29], d[25]>>2&3), GreenY: xy(d[30], d[25]&3),
		BlueX: xy(d[31], d[26]>>6), BlueY: xy(d[32], d[26]>>4&3),
		WhiteX: xy(d[33], d[26]>>2&3), WhiteY: xy(d[34], d[26]&3),
	}
	for i := 0; i < int(d[126]) && (i+2)*128 <= len(d); i++ {
		m.HDR.parseCTA(d[(i+1)*128 : (i+2)*128])
	}
	// Display descriptors: 0xFC name, 0xFF serial.
	for off := 54; off+18 <= 126; off += 18 {
		b := d[off : off+18]
		if b[0] != 0 || b[1] != 0 {
			continue
		}
		text := strings.TrimRight(strings.SplitN(string(b[5:18]), "\n", 2)[0], " ")
		switch b[3] {
		case 0xfc:
			m.Model = text
		case 0xff:
			m.Serial = text
		}
	}
	// Without descriptors, the product code and serial number stand in,
	// as libdisplay-info (and so niri) does: monitor keys match niri's.
	if m.Model == "Unknown" {
		if code := uint16(d[10]) | uint16(d[11])<<8; code != 0 {
			m.Model = fmt.Sprintf("0x%04X", code)
		}
	}
	if m.Serial == "" {
		if n := binary.LittleEndian.Uint32(d[12:16]); n != 0 {
			m.Serial = fmt.Sprintf("0x%08X", n)
		}
	}
	return m
}

// parseCTA skips malformed data blocks rather than reading into the DTD area.
func (h *HDRMetadata) parseCTA(b []byte) {
	if len(b) < 128 || b[0] != 0x02 || b[2] < 4 || b[2] > 127 {
		return
	}
	for off := 4; off < int(b[2]); {
		size := int(b[off] & 0x1f)
		end := off + 1 + size
		if end > int(b[2]) {
			return
		}
		if b[off]>>5 == 7 && size >= 2 {
			v := b[off+1 : end]
			switch v[0] {
			case 5: // colorimetry: BT2020RGB bit 7, BT2020YCC bit 6
				h.BT2020RGB = h.BT2020RGB || v[1]&0x80 != 0
				h.BT2020YCC = h.BT2020YCC || v[1]&0x40 != 0
			case 6: // HDR static metadata: EOTF, descriptors, luminances
				if len(v) < 3 {
					break
				}
				h.SDR = h.SDR || v[1]&1 != 0
				h.PQ = h.PQ || v[1]&4 != 0
				h.HLG = h.HLG || v[1]&8 != 0
				h.StaticType1 = h.StaticType1 || v[2]&1 != 0
				if len(v) > 3 && v[3] != 0 {
					h.MaxLuminance = 50 * math.Exp2(float64(v[3])/32)
				}
				if len(v) > 4 && v[4] != 0 {
					h.MaxFrameAverage = 50 * math.Exp2(float64(v[4])/32)
				}
				if len(v) > 5 && h.MaxLuminance != 0 {
					h.MinLuminance = h.MaxLuminance * math.Pow(float64(v[5])/255, 2) / 100
				}
			}
		}
		off = end
	}
}

// pnpName maps a PNP id to a vendor name via hwdata, falling back to the id.
func pnpName(id string) string {
	f, err := os.Open("/usr/share/hwdata/pnp.ids")
	if err != nil {
		return id
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if name, ok := strings.CutPrefix(s.Text(), id+"\t"); ok {
			return name
		}
	}
	return id
}
