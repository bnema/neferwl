package wayland

import (
	"strconv"
	"strings"
)

// minKeycodeMax is the smallest keycode maximum sent to clients.
// Xwayland (xkb/xkbUtils.c _XkbCopyClientMap, still in 24.1) clears
// MAP_LENGTH-(max+1) entries starting at that same index of its 256-entry
// key maps: with a maximum below 127 it writes past the end of the heap
// block and aborts on the next realloc. Dictation tools (wtype-like)
// send keymaps holding only the keys they type (e.g. 8..26). Keycodes up
// to 255 are always valid, so the range is widened without changing any
// key.
const minKeycodeMax = 255

// widenKeycodes raises the keycode maximum of a keymap's xkb_keycodes
// section to minKeycodeMax. An explicit maximum below it is rewritten; a
// section without one (its highest keycode is the maximum) gets one,
// unless it includes other files or already has a keycode above it. It
// returns the keymap and the former maximum (the highest keycode when
// none was set), 0 when unchanged. Keywords are case-insensitive;
// comments, strings and key names are skipped.
func widenKeycodes(text string) (string, int) {
	open, end := keycodesSection(text)
	if open < 0 {
		return text, 0
	}
	type span struct{ from, to int }
	var low []span
	maxSet, include := false, false
	from, highest := 0, 0
	lastName := false // the previous token was a <name>: "= N" is a keycode
	for i := open + 1; i < end; {
		if j := skipNoise(text, i); j != i {
			lastName = text[i] == '<'
			i = j
			continue
		}
		c := text[i]
		switch {
		case isIdentStart(c):
			j := i
			for j < end && isIdent(text[j]) {
				j++
			}
			word := text[i:j]
			lastName = false
			i = j
			switch {
			case strings.EqualFold(word, "include"), strings.EqualFold(word, "augment"), strings.EqualFold(word, "override"), strings.EqualFold(word, "replace"):
				include = true
			case strings.EqualFold(word, "maximum"):
				k := skipBlank(text, j, end)
				if k >= end || text[k] != '=' {
					continue
				}
				k = skipBlank(text, k+1, end)
				d := k
				for d < end && text[d] >= '0' && text[d] <= '9' {
					d++
				}
				n, err := strconv.Atoi(text[k:d])
				if err != nil {
					continue
				}
				maxSet = true
				if n < minKeycodeMax {
					low = append(low, span{k, d})
					from = n
				}
				i = d
			}
		case c == '=' && lastName:
			k := skipBlank(text, i+1, end)
			d := k
			for d < end && text[d] >= '0' && text[d] <= '9' {
				d++
			}
			if n, err := strconv.Atoi(text[k:d]); err == nil {
				highest = max(highest, n)
			}
			lastName = false
			i = max(d, i+1)
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				lastName = false
			}
			i++
		}
	}
	if !maxSet {
		if include || highest >= minKeycodeMax || highest == 0 {
			return text, 0
		}
		return text[:open+1] + " maximum = " + strconv.Itoa(minKeycodeMax) + ";" + text[open+1:], highest
	}
	if len(low) == 0 {
		return text, 0
	}
	var b strings.Builder
	last := 0
	for _, sp := range low {
		b.WriteString(text[last:sp.from])
		b.WriteString(strconv.Itoa(minKeycodeMax))
		last = sp.to
	}
	b.WriteString(text[last:])
	return b.String(), from
}

// keycodesSection returns the indexes of the braces of the first
// xkb_keycodes section, -1 without one.
func keycodesSection(text string) (open, end int) {
	for i := 0; i < len(text); {
		if j := skipNoise(text, i); j != i {
			i = j
			continue
		}
		if !isIdentStart(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isIdent(text[j]) {
			j++
		}
		if !strings.EqualFold(text[i:j], "xkb_keycodes") {
			i = j
			continue
		}
		// The section's name may be a string; its body is the next brace.
		for i = j; i < len(text) && text[i] != '{'; {
			if k := skipNoise(text, i); k != i {
				i = k
				continue
			}
			if text[i] == ';' {
				return -1, -1 // a declaration without a body
			}
			i++
		}
		if i >= len(text) {
			return -1, -1
		}
		open, depth := i, 0
		for i < len(text) {
			if k := skipNoise(text, i); k != i {
				i = k
				continue
			}
			switch text[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return open, i
				}
			}
			i++
		}
		return -1, -1
	}
	return -1, -1
}

// skipNoise returns the index after a comment (// or # to the end of the
// line), a string or a <key name> starting at i, else i.
func skipNoise(text string, i int) int {
	switch {
	case text[i] == '#', strings.HasPrefix(text[i:], "//"):
		if n := strings.IndexByte(text[i:], '\n'); n >= 0 {
			return i + n + 1
		}
		return len(text)
	case text[i] == '"':
		for j := i + 1; j < len(text); j++ {
			switch text[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return len(text)
	case text[i] == '<':
		if n := strings.IndexByte(text[i:], '>'); n >= 0 {
			return i + n + 1
		}
		return len(text)
	}
	return i
}

// skipBlank skips spaces and comments from i, up to end.
func skipBlank(text string, i, end int) int {
	for i < end {
		switch {
		case text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r':
			i++
		case text[i] == '#' || strings.HasPrefix(text[i:], "//"):
			i = skipNoise(text, i)
		default:
			return i
		}
	}
	return i
}

func isIdentStart(c byte) bool { return c == '_' || c|0x20 >= 'a' && c|0x20 <= 'z' }
func isIdent(c byte) bool      { return isIdentStart(c) || c >= '0' && c <= '9' }
