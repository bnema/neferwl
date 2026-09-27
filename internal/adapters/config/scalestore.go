package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ScaleStore writes output.<name>.scale into the config file, preserving
// unrelated lines and comments.
type ScaleStore struct{ Path string }

// SaveOutputScale updates output.<name>.scale in the config file. The file is
// replaced atomically; a symlinked config is written through to its target.
func (s ScaleStore) SaveOutputScale(output string, scale float64) error {
	path := s.Path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// Replacing a dangling link would lose it: report it instead.
		if path, err = filepath.EvalSymlinks(path); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	// A new config may hold commands: only its owner reads it.
	mode := os.FileMode(0o600)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	}
	return writeAtomic(path, setKey(data, "output."+output+".scale", formatScale(scale)), mode)
}

// setKey replaces the last active assignment, or enables a matching commented
// assignment. New keys go beside the closest related key, then at the end.
func setKey(data []byte, key, value string) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	commented := -1
	for i, line := range lines {
		body := strings.TrimSpace(line)
		if !strings.HasPrefix(body, "#") {
			continue
		}
		body = strings.TrimSpace(strings.TrimPrefix(body, "#"))
		k, _, ok := strings.Cut(body, "=")
		if ok && strings.TrimSpace(k) == key {
			commented = i
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		prefix, rest, ok := strings.Cut(lines[i], "=")
		if !ok || strings.TrimSpace(prefix) != key {
			continue
		}
		lines[i] = replaceValue(prefix+"=", rest, value)
		return []byte(strings.Join(lines, ""))
	}
	if commented >= 0 {
		line := lines[commented]
		indent, body, _ := strings.Cut(line, "#")
		prefix, rest, _ := strings.Cut(strings.TrimLeft(body, " \t"), "=")
		lines[commented] = replaceValue(indent+prefix+"=", rest, value)
		return []byte(strings.Join(lines, ""))
	}
	// Match the longest dotted parent first: output.DP-2.scale belongs beside
	// output.DP-2.*, not merely beside any output.* key.
	for parent := key; ; {
		dot := strings.LastIndexByte(parent, '.')
		if dot < 0 {
			break
		}
		parent = parent[:dot]
		index := -1
		for i, line := range lines {
			k, _, ok := strings.Cut(line, "=")
			k = strings.TrimSpace(k)
			if ok && (k == parent || strings.HasPrefix(k, parent+".")) {
				index = i
			}
		}
		if index < 0 {
			continue
		}
		eol := "\n"
		if strings.HasSuffix(lines[index], "\r\n") {
			eol = "\r\n"
		}
		if !strings.HasSuffix(lines[index], "\n") {
			lines[index] += eol
		}
		lines = append(lines, "")
		copy(lines[index+2:], lines[index+1:])
		lines[index+1] = key + " = " + value + eol
		return []byte(strings.Join(lines, ""))
	}
	var b bytes.Buffer
	b.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s = %s\n", key, value)
	return b.Bytes()
}

// replaceValue keeps indentation, spacing and trailing comments in an assignment.
func replaceValue(prefix, rest, value string) string {
	plain := strings.TrimRight(rest, "\r\n")
	eol := rest[len(plain):]
	old := stripComment(strings.TrimSpace(plain))
	start := strings.Index(plain, old)
	return prefix + plain[:start] + value + plain[start+len(old):] + eol
}

// formatScale writes scales as the config expects them: a decimal when it is
// exact (1.25), else a fraction (4/3).
func formatScale(s float64) string {
	n, d := int(math.Round(s*120)), 120
	g := gcd(n, d)
	n, d = n/g, d/g
	den := d
	for den%2 == 0 {
		den /= 2
	}
	for den%5 == 0 {
		den /= 5
	}
	if den == 1 {
		return strconv.FormatFloat(float64(n)/float64(d), 'f', -1, 64)
	}
	return fmt.Sprintf("%d/%d", n, d)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// writeAtomic replaces path with data: readers and the watcher never see a
// half-written file.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
