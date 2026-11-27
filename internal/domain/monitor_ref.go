package domain

// MonitorRef remembers a monitor by its connector and EDID fingerprint, so a
// replugged monitor can be found again on another connector.
type MonitorRef struct {
	Connector, EDIDID, EDIDHash string
}

// Ref returns the monitor identity of the output.
func (i OutputInfo) Ref() MonitorRef {
	return MonitorRef{Connector: i.Name, EDIDID: i.EDIDID, EDIDHash: i.EDIDHash}
}

// Physical reports whether the output behaves like a real monitor: a
// physical connector, or a virtual output given an EDID identity.
func (i OutputInfo) Physical() bool { return !i.Virtual || i.EDIDID != "" }

// ValidEDIDID reports whether s is 1–64 characters of [0-9a-zA-Z._-].
func ValidEDIDID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
