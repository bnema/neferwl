package xwayland

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
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

// Owns recognizes the leader and a process in its session, but no other PID.
func TestOwns(t *testing.T) {
	pid := os.Getpid()
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	d := &Display{}
	if d.Owns(pid) {
		t.Fatal("owned without a satellite")
	}
	d.satellite.Store(int64(pid))
	if !d.Owns(pid) {
		t.Fatal("satellite not owned")
	}
	d.satellite.Store(int64(sid))
	if !d.Owns(pid) {
		t.Fatal("session member not owned")
	}
	if d.Owns(0) || d.Owns(-1) || d.Owns(1<<30) {
		t.Fatal("invalid or missing process owned")
	}
	d.satellite.Store(int64(sid + 1))
	if d.Owns(pid) {
		t.Fatal("unrelated session owned")
	}
	d.satellite.Store(0)
	if d.Owns(pid) {
		t.Fatal("owned after satellite exit")
	}
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

// A satellite that exits without accepting drops the waiting client and is
// not respawned until another client connects.
//
//testpolicy:realtime real X11 sockets and satellite subprocess lifetime
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
	// Go runtime signals (preemption) can interrupt the raw read: retry.
	n, err := unix.Read(fd, make([]byte, 1))
	for errors.Is(err, unix.EINTR) {
		n, err = unix.Read(fd, make([]byte, 1))
	}
	if n != 0 || err != nil {
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

// satelliteRuns reads the lines the satellite script writes to a FIFO. The
// test holds the FIFO read-write, so a writer never blocks and the read never
// sees EOF.
type satelliteRuns struct {
	lines chan string
	got   []string
}

// wait returns the runs once n were recorded, within one deadline; fewer
// when the deadline expires.
//
//testpolicy:realtime real X11 sockets and satellite subprocess lifetime
func (r *satelliteRuns) wait(n int) []string {
	deadline := time.After(3 * time.Second)
	for len(r.got) < n {
		select {
		case l := <-r.lines:
			r.got = append(r.got, l)
		case <-deadline:
			return r.got
		}
	}
	return r.settled()
}

// settled returns the runs recorded so far, without waiting.
func (r *satelliteRuns) settled() []string {
	for {
		select {
		case l := <-r.lines:
			r.got = append(r.got, l)
		default:
			return r.got
		}
	}
}

// satelliteScript writes a real script that records each run and whether it
// received listening sockets on fds 3 and 4, then exits.
func satelliteScript(t *testing.T, dir string) (string, *satelliteRuns) {
	t.Helper()
	fifo := filepath.Join(dir, "runs")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	runs := &satelliteRuns{lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			runs.lines <- strings.ReplaceAll(sc.Text(), " ", "_")
		}
	}()
	t.Cleanup(func() { _ = f.Close() })
	bin := filepath.Join(dir, "satellite")
	script := "#!/bin/sh\n[ -S /proc/self/fd/3 ] && [ -S /proc/self/fd/4 ] && echo \"$*\" >> " + fifo + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, runs
}

// Each connection starts the satellite once, with both sockets handed over.
//
//testpolicy:realtime real X11 sockets and satellite subprocess lifetime
func TestRunStartsSatelliteOnConnect(t *testing.T) {
	opts := testOptions(t, "")
	var runs *satelliteRuns
	opts.Binary, runs = satelliteScript(t, opts.TmpDir)
	opts.Abstract = true
	d, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	<-time.After(100 * time.Millisecond)
	if got := runs.settled(); len(got) != 0 {
		t.Fatalf("started without a client: %v", got)
	}
	unix.Close(dial(t, d.socket))
	want := d.Name() + "_-listenfd_3_-listenfd_4"
	if got := runs.wait(1); len(got) != 1 || got[0] != want {
		t.Fatalf("runs %v, want [%s]", got, want)
	}
	// The next client, after the backoff, starts it again.
	<-time.After(1200 * time.Millisecond)
	if got := runs.settled(); len(got) != 1 {
		t.Fatalf("respawned without a client: %v", got)
	}
	unix.Close(dial(t, "\x00"+d.socket))
	if got := runs.wait(2); len(got) != 2 {
		t.Fatalf("runs %v after the second client", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStaleLockReclaimed(t *testing.T) {
	opts := testOptions(t, "true")
	// PID 0x7ffffff0 does not exist.
	if err := os.WriteFile(filepath.Join(opts.TmpDir, ".X0-lock"), []byte("2147483632\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	// A live process keeps its display.
	if err := os.WriteFile(filepath.Join(opts.TmpDir, ".X1-lock"), []byte(strconv.Itoa(os.Getpid())), 0o444); err != nil {
		t.Fatal(err)
	}
	d, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Name() != ":0" {
		t.Fatalf("display %s", d.Name())
	}
	if b, _ := os.ReadFile(d.lock); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock holds %q", b)
	}
}

func TestDirChecks(t *testing.T) {
	for name, setup := range map[string]func(dir string) error{
		"not sticky": func(dir string) error { return os.Mkdir(dir, 0o777) },
		"symlink": func(dir string) error {
			target := dir + "-target"
			if err := os.Mkdir(target, 0o1777); err != nil {
				return err
			}
			return os.Symlink(target, dir)
		},
		"file": func(dir string) error { return os.WriteFile(dir, nil, 0o777) },
	} {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t, "true")
			if err := setup(opts.Dir); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(opts.Dir, 0o777|os.ModeSticky)
			if name == "not sticky" {
				_ = os.Chmod(opts.Dir, 0o777)
			}
			if _, err := Open(opts); err == nil {
				t.Fatal("opened an unsafe X11 directory")
			}
		})
	}
}

func TestSupported(t *testing.T) {
	if !Supported(context.Background(), "true") || Supported(context.Background(), "false") {
		t.Fatal("exit status not honoured")
	}
	hang := filepath.Join(t.TempDir(), "hang")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if Supported(context.Background(), hang) || time.Since(start) > 3*time.Second {
		t.Fatalf("hanging binary: %v", time.Since(start))
	}
}

// Stopping kills the satellite's whole process group, even when a child
// keeps its stderr open.
//
//testpolicy:realtime real X11 sockets and satellite subprocess lifetime
func TestRunKillsSatelliteGroup(t *testing.T) {
	opts := testOptions(t, "")
	pidfile := filepath.Join(opts.TmpDir, "pid")
	if err := unix.Mkfifo(pidfile, 0o600); err != nil {
		t.Fatal(err)
	}
	pidfifo, err := os.OpenFile(pidfile, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pidfifo.Close()
	opts.Binary = filepath.Join(opts.TmpDir, "satellite")
	script := "#!/bin/sh\ntrap '' TERM\nsleep 30 &\necho $! > " + pidfile + "\nwait\n"
	if err := os.WriteFile(opts.Binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	unix.Close(dial(t, d.socket))
	// The script writes the child's pid to the FIFO the test holds
	// read-write; the read deadline bounds the wait.
	if err := pidfifo.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(pidfifo).ReadString('\n')
	if err != nil {
		t.Fatalf("satellite did not start: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(line))
	if pid <= 0 {
		t.Fatalf("satellite pid %q", line)
	}
	// A pidfd of the grandchild becomes readable when it exits.
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, 2000)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("child %d survived", pid)
		}
		break
	}
}
