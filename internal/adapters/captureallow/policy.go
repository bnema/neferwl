// Package captureallow reads the screen-capture allowlist: the executables
// that may capture the screen. The file is /etc/neferwl/capture-allow, one
// absolute executable path per line; it is root-owned and fixed, never read
// from the user config that a program of the same user could edit.
package captureallow

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strings"
)

// DefaultPath is where the allowlist lives.
const DefaultPath = "/etc/neferwl/capture-allow"

// maxSize bounds what is read: a bigger file is invalid.
const maxSize = 1 << 20

// Mode is how a policy decides.
type Mode int

const (
	// ModeList allows the listed executables only.
	ModeList Mode = iota
	// ModeDisabled allows every client: a `*` line.
	ModeDisabled
	// ModeClosed allows no client: the file is unreadable or invalid.
	ModeClosed
)

// Policy is an immutable allowlist. The zero value allows nothing.
type Policy struct {
	mode Mode
	exes map[string]struct{}
}

// Builtin returns the built-in list policy, used when the file is missing. A
// file replaces the list entirely. The list is built on each call, so no
// package state can be changed.
func Builtin() *Policy {
	builtin := [...]string{
		"/usr/bin/grim",
		"/usr/bin/nefercap",
		"/usr/lib/xdg-desktop-portal-wlr",
		"/usr/libexec/xdg-desktop-portal-wlr",
	}
	p := &Policy{mode: ModeList, exes: make(map[string]struct{}, len(builtin))}
	for _, e := range builtin {
		p.exes[e] = struct{}{}
	}
	return p
}

// Disabled returns the policy that lets every client capture.
func Disabled() *Policy { return &Policy{mode: ModeDisabled} }

// Closed returns the policy that lets no client capture.
func Closed() *Policy { return &Policy{mode: ModeClosed} }

// Mode reports how the policy decides.
func (p *Policy) Mode() Mode { return p.mode }

// Len is the number of listed executables.
func (p *Policy) Len() int { return len(p.exes) }

func (p *Policy) equal(o *Policy) bool {
	return p.mode == o.mode && maps.Equal(p.exes, o.exes)
}

// Allows reports whether the executable at path may capture. The comparison
// is exact, on the cleaned absolute path.
func (p *Policy) Allows(exe string) bool {
	switch p.mode {
	case ModeDisabled:
		return true
	case ModeClosed:
		return false
	}
	if !filepath.IsAbs(exe) {
		return false
	}
	_, ok := p.exes[filepath.Clean(exe)]
	return ok
}

// ParseError names the offending line of an invalid file.
type ParseError struct {
	Line int
	Text string
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s: %q", e.Line, e.Msg, e.Text)
}

// Parse reads the file format:
//
//   - blank lines and lines starting with `#` are ignored;
//   - a line `*` alone disables the check;
//   - any other line is an absolute executable path, taken whole after
//     trimming surrounding white space (a `#` inside a line is part of the
//     path).
//
// Any other content is an error naming its line; the policy is then nil.
func Parse(r io.Reader) (*Policy, error) {
	p := &Policy{mode: ModeList, exes: map[string]struct{}{}}
	all := false
	sc := bufio.NewScanner(io.LimitReader(r, maxSize+1))
	sc.Buffer(make([]byte, 0, 4096), maxSize+1)
	n, read := 0, 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		read += len(raw) + 1
		if read > maxSize {
			return nil, &ParseError{Line: n, Text: clip(raw), Msg: "file too large"}
		}
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case line == "*":
			all = true
		case !filepath.IsAbs(line) || strings.ContainsRune(line, 0) || strings.HasSuffix(line, "/"):
			return nil, &ParseError{Line: n, Text: clip(line), Msg: "want an absolute executable path"}
		default:
			p.exes[filepath.Clean(line)] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, &ParseError{Line: n + 1, Msg: "file too large"}
		}
		return nil, err
	}
	if all {
		return Disabled(), nil
	}
	return p, nil
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
