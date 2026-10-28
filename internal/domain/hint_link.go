package domain

import (
	"net/url"
	"strings"
)

// MaxHintURLLen is the maximum accepted length of a hint link, in bytes.
const MaxHintURLLen = 2048

// ValidHintURL reports whether s is a safe link to open from a hint target.
// It must be 1..MaxHintURLLen bytes of restricted printable ASCII and start
// with lowercase "https://", "http://" or "mailto:". http(s) links need a host
// and no userinfo; mailto links need a single local@domain address without
// query or fragment.
func ValidHintURL(s string) bool {
	if len(s) == 0 || len(s) > MaxHintURLLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !hintURLByte(s[i]) {
			return false
		}
	}
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		u, err := url.Parse(s)
		return err == nil && u.Hostname() != "" && u.User == nil
	case strings.HasPrefix(s, "mailto:"):
		rest := s[len("mailto:"):]
		if strings.ContainsAny(rest, "?&#") || strings.Count(rest, "@") != 1 {
			return false
		}
		local, domain, _ := strings.Cut(rest, "@")
		return local != "" && domain != ""
	}
	return false
}

func hintURLByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-._~:/?#[]@!$&'()*+,;=%", c) >= 0
}
