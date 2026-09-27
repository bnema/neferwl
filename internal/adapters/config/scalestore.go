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

// ScaleStore writes output.<name>.scale into the config file. It edits the
// value in place and keeps every other line, comments included.
type ScaleStore struct{ Path string }

// SaveOutputScale sets output.<name>.scale, appending the key when the file
// has none. The file is replaced atomically; a symlinked config is written
// through to its target.
func (s ScaleStore) SaveOutputScale(output string, scale float64) error {
	path := s.Path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// Replacing a dangling link would lose it: report it instead.
		if path, err = filepath.EvalSymlinks(path); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	mode := os.FileMode(0o644)
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

// setKey replaces the value of the last line setting key (the one parse
// keeps) or appends key = value.
func setKey(data []byte, key, value string) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(lines[i], "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		// Keep the spacing around = and an end-of-line comment.
		rest := strings.TrimRight(v, "\r\n")
		eol := v[len(rest):]
		old := stripComment(strings.TrimSpace(rest))
		start := strings.Index(rest, old)
		lines[i] = k + "=" + rest[:start] + value + rest[start+len(old):] + eol
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
