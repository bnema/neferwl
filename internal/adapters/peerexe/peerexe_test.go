package peerexe

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
func TestExeOfPidfdDeletedAndExited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "child")
	src, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := os.StartProcess(path, []string{path, "30"}, &os.ProcAttr{})
	if err != nil {
		t.Skip("cannot run a copy of sleep: ", err)
	}
	defer func() { _ = proc.Kill(); _, _ = proc.Wait() }()
	_ = os.Remove(path)
	pidfd, err := unix.PidfdOpen(proc.Pid, 0)
	if err != nil {
		t.Skip("pidfd_open: ", err)
	}
	defer unix.Close(pidfd)
	// Wait until exec has completed so /proc/<pid>/exe is the copy.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, err := ExeOfPidfd(pidfd); errors.Is(err, ErrExeDeleted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deleted executable not refused")
		}
		time.Sleep(5 * time.Millisecond)
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
