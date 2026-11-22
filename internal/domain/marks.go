package domain

import (
	"slices"
	"strings"
)

// maxMarkName caps the length of a mark name in bytes.
const maxMarkName = 64

// MarkSet is the set of marks of one window in canonical form: valid names,
// sorted, unique, joined by single spaces; "" is the empty set. It is a
// string so WindowState stays comparable with ==.
type MarkSet string

// ValidMarkName reports whether s is 1 to 64 bytes of [A-Za-z0-9_-].
func ValidMarkName(s string) bool {
	if s == "" || len(s) > maxMarkName {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_', b == '-':
		default:
			return false
		}
	}
	return true
}

// Empty reports whether the set holds no mark.
func (m MarkSet) Empty() bool { return m == "" }

// Names returns the marks in order, as a fresh non-nil slice.
func (m MarkSet) Names() []string {
	if m == "" {
		return []string{}
	}
	return strings.Split(string(m), " ")
}

// Has reports whether name is in the set.
func (m MarkSet) Has(name string) bool {
	return m != "" && slices.Contains(strings.Split(string(m), " "), name)
}

// With returns the set plus name; an invalid name leaves it unchanged.
func (m MarkSet) With(name string) MarkSet {
	if !ValidMarkName(name) || m.Has(name) {
		return m
	}
	names := append(m.Names(), name)
	slices.Sort(names)
	return MarkSet(strings.Join(names, " "))
}

// Without returns the set minus name.
func (m MarkSet) Without(name string) MarkSet {
	if !m.Has(name) {
		return m
	}
	names := slices.DeleteFunc(m.Names(), func(n string) bool { return n == name })
	return MarkSet(strings.Join(names, " "))
}
