package wayland

import (
	"bnema.dev/zerowrap"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestListen(t *testing.T) {
	dir := t.TempDir()
	n1, fd1, close1, err := listen(dir, zerowrap.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	defer close1()
	defer func() { _ = unix.Close(fd1) }()
	n2, fd2, close2, err := listen(dir, zerowrap.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd2) }()
	if n1 != "wayland-1" || n2 != "wayland-2" {
		t.Fatalf("names: %s, %s", n1, n2)
	}
	close2()
	for _, suffix := range []string{"", ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, n2+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", n2+suffix, err)
		}
	}
	close1()
	for _, suffix := range []string{"", ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, n1+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", n1+suffix, err)
		}
	}
}

// No real-time waits: connect completes against the kernel listening backlog.
func TestListenPublishedSocket(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(fmt.Sprint(long), func(t *testing.T) {
			dir := t.TempDir()
			if long {
				dir = socketPathDir(t, 107)
			}
			tmp := filepath.Join(dir, ".wl-1")
			if err := os.WriteFile(tmp, []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
			name, fd, cleanup, err := listen(dir, zerowrap.Logger{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(fd) }()
			defer cleanup()
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].Name() != name || entries[1].Name() != name+".lock" {
				t.Fatalf("unexpected published files: %v", entries)
			}
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("not a socket: %v", info.Mode())
			}
			client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(client) }()
			if err := unix.Connect(client, &unix.SockaddrUnix{Name: path}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// socketPathDir avoids depending on TMPDIR or the length of the test name.
func socketPathDir(t *testing.T, pathLen int) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	padding := pathLen - len(root) - len("//wayland-1")
	if padding < 1 {
		t.Skip("temporary root too long for socket boundary fixture")
	}
	dir := filepath.Join(root, strings.Repeat("x", padding))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got := len(filepath.Join(dir, "wayland-1")); got != pathLen {
		t.Fatalf("public path length = %d, want %d", got, pathLen)
	}
	return dir
}

func TestListenRejectsUnreachablePath(t *testing.T) {
	dir := socketPathDir(t, 108)
	_, fd, cleanup, err := listen(dir, zerowrap.Logger{})
	if err == nil || fd != -1 || cleanup != nil {
		t.Fatalf("unexpected result: fd=%d err=%v", fd, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected path left files: %v", entries)
	}
}

func TestListenErrorCleansStaging(t *testing.T) {
	dir := t.TempDir()
	// A nonempty destination cannot be removed before binding.
	path := filepath.Join(dir, "wayland-1")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".wl-1")
	if err := os.WriteFile(tmp, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, fd, cleanup, err := listen(dir, zerowrap.Logger{})
	if err == nil || fd != -1 || cleanup != nil {
		t.Fatalf("unexpected result: fd=%d err=%v", fd, err)
	}
	for _, p := range []string{tmp, path + ".lock"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", p, err)
		}
	}
}
