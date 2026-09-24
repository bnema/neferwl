package drm

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Monitor identifies the display attached to a connector.
type Monitor struct {
	Make, Model, Serial string
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
	return m
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
