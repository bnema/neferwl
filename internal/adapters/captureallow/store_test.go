package captureallow

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// logBuf is a locked bytes.Buffer, a real io.Writer.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testLog(out *logBuf) zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: out}).WithField("component", "captureallow")
}

// uid owns every file the tests create, standing in for root.
var uid = uint32(os.Getuid())

func newTestStore(path string, log zerowrap.Logger) *Store { return NewStoreOwnedBy(path, uid, log) }

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p, err := load(filepath.Join(dir, "missing"), uid)
	if !errors.Is(err, fs.ErrNotExist) || p.Mode() != ModeList || !p.Allows("/usr/bin/grim") || p.Allows("/usr/bin/other") {
		t.Fatalf("missing: %v, %v", p.Mode(), err)
	}
	bad := filepath.Join(dir, "bad")
	_ = os.WriteFile(bad, []byte("/ok\nrelative\n"), 0o644)
	p, err = load(bad, uid)
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Line != 2 || p.Mode() != ModeClosed || p.Allows("/ok") {
		t.Fatalf("invalid: %v, %v", p.Mode(), err)
	}
	// A directory is unreadable as a file: fail closed, not disabled.
	p, err = load(dir, uid)
	if err == nil || p.Mode() != ModeClosed {
		t.Fatalf("directory: %v, %v", p.Mode(), err)
	}
}

func TestStoreStartupLogs(t *testing.T) {
	dir := t.TempDir()
	var out logBuf
	s := newTestStore(filepath.Join(dir, "capture-allow"), testLog(&out))
	if !s.Policy().Allows("/usr/bin/grim") || s.Policy().Allows("/any") {
		t.Fatal("missing file must give the built-in list")
	}
	log := out.String()
	if !strings.Contains(log, `"level":"info"`) || !strings.Contains(log, "capture allowlist file missing, using the built-in list") || !strings.Contains(log, `"component":"captureallow"`) || !strings.Contains(log, filepath.Join(dir, "capture-allow")) {
		t.Fatalf("log = %s", log)
	}
	bad := filepath.Join(dir, "bad")
	_ = os.WriteFile(bad, []byte("/ok\n\nnope\n"), 0o644)
	out = logBuf{}
	s = newTestStore(bad, testLog(&out))
	if s.Policy().Allows("/ok") {
		t.Fatal("invalid file must fail closed")
	}
	log = out.String()
	if !strings.Contains(log, `"level":"error"`) || !strings.Contains(log, `"line":3`) {
		t.Fatalf("log = %s", log)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout: " + what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStoreReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture-allow")
	var out logBuf
	s := newTestStore(path, testLog(&out))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	write := func(content string) {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil { // atomic, as packages and editors do
			t.Fatal(err)
		}
	}
	write("/usr/bin/grim\n")
	waitFor(t, "list", func() bool { return s.Policy().Mode() == ModeList && s.Policy().Allows("/usr/bin/grim") })
	if s.Policy().Allows("/usr/bin/other") {
		t.Fatal("unlisted allowed")
	}
	write("/usr/bin/other\nbroken\n")
	waitFor(t, "closed", func() bool { return s.Policy().Mode() == ModeClosed })
	write("*\n")
	waitFor(t, "disabled", func() bool { return s.Policy().Mode() == ModeDisabled })
	// In-place rewrite.
	if err := os.WriteFile(path, []byte("/usr/bin/third\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "in place", func() bool { return s.Policy().Allows("/usr/bin/third") && !s.Policy().Allows("/x") })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "removed", func() bool { return s.Policy().Allows("/usr/bin/grim") && !s.Policy().Allows("/usr/bin/third") })
	if s.Policy().Mode() != ModeList || s.Policy().Len() != Builtin().Len() {
		t.Fatal("deleting the file must restore the built-in list")
	}
}

func TestStoreWaitsForMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etc", "neferwl")
	path := filepath.Join(dir, "capture-allow")
	var out logBuf
	s := newTestStore(path, testLog(&out))
	s.retryInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(60 * time.Millisecond)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("/usr/bin/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "list after mkdir", func() bool { return s.Policy().Mode() == ModeList && s.Policy().Allows("/usr/bin/other") })
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // umask
		t.Fatal(err)
	}
}

// Every way the file or its directories can be untrustworthy fails closed.
func TestLoadRefusesUntrusted(t *testing.T) {
	good := func(t *testing.T) (dir, path string) {
		dir = filepath.Join(t.TempDir(), "neferwl")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, "capture-allow")
		mustWrite(t, path, "/usr/bin/grim\n", 0o644)
		return dir, path
	}
	if p, err := load(func() string { _, p := good(t); return p }(), uid); err != nil || !p.Allows("/usr/bin/grim") {
		t.Fatalf("trusted file: %v", err)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir, path string) string
		owner uint32
		want  string
	}{
		{"group writable file", func(t *testing.T, _, path string) string { mustWrite(t, path, "*\n", 0o664); return path }, uid, "writable by group or others"},
		{"other writable file", func(t *testing.T, _, path string) string { mustWrite(t, path, "*\n", 0o646); return path }, uid, "writable by group or others"},
		{"wrong owner", func(t *testing.T, _, path string) string { return path }, uid + 1, "owned by uid"},
		{"symlink file", func(t *testing.T, dir, path string) string {
			link := filepath.Join(dir, "link")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, uid, "symbolic link"},
		{"fifo", func(t *testing.T, dir, _ string) string {
			p := filepath.Join(dir, "fifo")
			if err := unix.Mkfifo(p, 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}, uid, "not a regular file"},
		{"group writable directory", func(t *testing.T, dir, path string) string {
			if err := os.Chmod(dir, 0o775); err != nil {
				t.Fatal(err)
			}
			return path
		}, uid, "writable by group or others"},
		{"symlinked directory", func(t *testing.T, dir, path string) string {
			link := filepath.Join(filepath.Dir(dir), "via")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "capture-allow")
		}, uid, "not a real directory"},
		{"directory owned by someone else", func(t *testing.T, _, path string) string { return path }, 1, "owned by uid"},
		{"missing ancestor", func(t *testing.T, dir, _ string) string {
			return filepath.Join(dir, "gone", "deeper", "capture-allow")
		}, uid, "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := good(t)
			path = tc.setup(t, dir, path)
			p, err := load(path, tc.owner)
			if err == nil || p.Mode() != ModeClosed || p.Allows("/usr/bin/grim") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mode %v, err %v; want closed, %q", p.Mode(), err, tc.want)
			}
		})
	}
	// A sticky, world-writable ancestor (like /tmp) is fine; the directory
	// holding the file being absent gives the built-in list.
	t.Run("missing holding directory", func(t *testing.T) {
		dir, _ := good(t)
		p, err := load(filepath.Join(dir, "sub", "capture-allow"), uid)
		if !errors.Is(err, fs.ErrNotExist) || p.Mode() != ModeList || !p.Allows("/usr/bin/grim") || p.Allows("/usr/bin/other") {
			t.Fatalf("mode %v, err %v", p.Mode(), err)
		}
	})
	t.Run("sticky directory", func(t *testing.T) {
		dir, path := good(t)
		if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
			t.Fatal(err)
		}
		if p, err := load(path, uid); err != nil || p.Mode() != ModeList {
			t.Fatalf("mode %v, err %v", p.Mode(), err)
		}
	})
}

// The real production check: this file is not root's, so load closes.
func TestLoadProductionOwner(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	path := filepath.Join(t.TempDir(), "capture-allow")
	mustWrite(t, path, "*\n", 0o644)
	if p, err := load(path, 0); err == nil || p.Mode() != ModeClosed {
		t.Fatalf("mode %v, err %v", p.Mode(), err)
	}
}

func TestEventAction(t *testing.T) {
	name := []byte("capture-allow\x00\x00\x00")
	for _, tc := range []struct {
		mask         uint32
		raw          []byte
		reload, lost bool
	}{
		{unix.IN_Q_OVERFLOW, nil, true, false},
		{unix.IN_IGNORED, nil, true, true},
		{unix.IN_CLOSE_WRITE, name, true, false},
		{unix.IN_ATTRIB, name, true, false},
		{unix.IN_ATTRIB, nil, true, false},
		{unix.IN_CLOSE_WRITE, []byte("other\x00\x00\x00"), false, false},
	} {
		if reload, lost := eventAction(tc.mask, tc.raw, "capture-allow"); reload != tc.reload || lost != tc.lost {
			t.Errorf("mask %#x %q = %v, %v; want %v, %v", tc.mask, tc.raw, reload, lost, tc.reload, tc.lost)
		}
	}
}

func TestStoreLogsUntrusted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture-allow")
	mustWrite(t, path, "*\n", 0o666)
	var out logBuf
	s := newTestStore(path, testLog(&out))
	if s.Policy().Allows("/any") || !strings.Contains(out.String(), `"level":"error"`) || !strings.Contains(out.String(), "writable by group or others") {
		t.Fatalf("log = %s", out.String())
	}
}
