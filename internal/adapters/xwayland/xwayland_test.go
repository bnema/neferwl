package xwayland

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/logging"
	"golang.org/x/sys/unix"
)

func testOptions(t *testing.T, binary string) Options {
	t.Helper()
	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o1777); err != nil {
		t.Fatal(err)
	}
	return Options{Binary: binary, Dir: filepath.Join(tmp, ".X11-unix"), TmpDir: tmp, Log: logging.For(context.Background(), "xwayland")}
}

func dial(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestOpenPicksFreeDisplay(t *testing.T) {
	opts := testOptions(t, "true")
	if err := os.WriteFile(filepath.Join(opts.TmpDir, ".X0-lock"), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	d, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != ":1" {
		t.Fatalf("display %s", d.Name())
	}
	lock, err := os.ReadFile(filepath.Join(opts.TmpDir, ".X1-lock"))
	if err != nil || strings.TrimSpace(string(lock)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock %q %v", lock, err)
	}
	st, err := os.Stat(opts.Dir)
	if err != nil || st.Mode()&os.ModeSticky == 0 || st.Mode().Perm() != 0o777 {
		t.Fatalf("dir %v %v", st.Mode(), err)
	}
	d.Close()
	for _, p := range []string{filepath.Join(opts.Dir, "X1"), filepath.Join(opts.TmpDir, ".X1-lock")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", p, err)
		}
	}
}

func TestInsecureDirRejected(t *testing.T) {
	opts := testOptions(t, "true")
	if err := os.Mkdir(opts.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(opts); err == nil {
		t.Fatal("opened a non-sticky X11 directory")
	}
}

// A satellite that exits without accepting drops the waiting client and is
// not respawned until another client connects.
func TestRunDropsClientsOfFailedSatellite(t *testing.T) {
	d, err := Open(testOptions(t, "false"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	fd := dial(t, d.socket)
	defer unix.Close(fd)
	// The dropped connection reads EOF.
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 3}); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Read(fd, make([]byte, 1)); n != 0 || err != nil {
		t.Fatalf("read %d %v, want EOF", n, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}
