package peerexe

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// The real lookup, against a socket whose peer is this process.
func TestPidfdSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	pidfd, err := Pidfd(fds[0])
	if errors.Is(err, ErrNoPidfd) {
		t.Skip("kernel without SO_PEERPIDFD")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer pidfd.Close()
	exe, pid, err := ExeOfPidfd(int(pidfd.Fd()))
	runtime.KeepAlive(pidfd)
	if err != nil {
		t.Fatal(err)
	}
	if exe != self || pid != os.Getpid() {
		t.Fatalf("ExeOfPidfd = %q, %d; want %q, %d", exe, pid, self, os.Getpid())
	}
}

// Open of the own pid names the own executable.
func TestOpenSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidfd, err := Open(os.Getpid())
	if err != nil {
		t.Skip("pidfd_open: ", err)
	}
	defer pidfd.Close()
	exe, pid, err := ExeOfPidfd(int(pidfd.Fd()))
	runtime.KeepAlive(pidfd)
	if err != nil {
		t.Fatal(err)
	}
	if exe != self || pid != os.Getpid() {
		t.Fatalf("ExeOfPidfd = %q, %d; want %q, %d", exe, pid, self, os.Getpid())
	}
}

func TestOpenMissingProcess(t *testing.T) {
	// Pids above the kernel's pid_max do not exist.
	if f, err := Open(1 << 30); err == nil {
		f.Close()
		t.Fatal("Open of a missing pid succeeded")
	}
}

// A process whose binary was removed is refused, and a peer that already
// exited is never resolved from its (possibly recycled) pid.
//
//testpolicy:realtime real child process; its post-exec readiness line is read from a pipe
func TestExeOfPidfdDeletedAndExited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "child")
	src, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh")
	}
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdoutR.Close()
	// The copied shell prints once it runs, then blocks reading stdin.
	proc, err := os.StartProcess(path, []string{path, "-c", "echo ready; read _"}, &os.ProcAttr{Files: []*os.File{stdinR, stdoutW, nil}})
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		t.Skip("cannot run a copy of sh: ", err)
	}
	defer func() { _ = proc.Kill(); _, _ = proc.Wait() }()
	_ = os.Remove(path)
	pidfd, err := unix.PidfdOpen(proc.Pid, 0)
	if err != nil {
		t.Skip("pidfd_open: ", err)
	}
	defer unix.Close(pidfd)
	// StartProcess may return before /proc/<pid>/exe names the copy; the
	// line is written by the copied shell itself, so exec has completed.
	line := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stdoutR, line); err != nil || string(line) != "ready\n" {
		t.Fatalf("child readiness: %q, %v", line, err)
	}
	if _, _, err := ExeOfPidfd(pidfd); !errors.Is(err, ErrExeDeleted) {
		t.Fatalf("deleted executable not refused: %v", err)
	}

	if _, err := readExe(proc.Pid); !errors.Is(err, ErrExeDeleted) {
		t.Fatalf("readExe: %v", err)
	}
	_ = proc.Kill()
	_, _ = proc.Wait()
	if exe, _, err := ExeOfPidfd(pidfd); err == nil {
		t.Fatalf("exited peer resolved to %q", exe)
	}
}
