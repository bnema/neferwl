// Package peerexe names the executable behind a process through a pidfd.
//
// A pidfd pins its process: the pid cannot be recycled while the descriptor
// is open. ExeOfPidfd reads the executable through that pin and proves the
// process still existed after the read, so the answer is never the exe of a
// process that took over a recycled pid. There is no fallback to a bare pid,
// which has no such proof.
package peerexe

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	// ErrExeDeleted: the executable of the process was removed.
	ErrExeDeleted = errors.New("executable deleted")
	// ErrNoPidfd is a kernel without SO_PEERPIDFD (before Linux 6.5): the
	// peer cannot be identified safely.
	ErrNoPidfd = errors.New("kernel has no SO_PEERPIDFD")
)

// Pidfd returns a pidfd of the peer of the connected unix socket sockFD
// (SO_PEERPIDFD). The caller closes it. ErrNoPidfd means the kernel cannot
// give one.
func Pidfd(sockFD int) (*os.File, error) {
	pidfd, err := unix.GetsockoptInt(sockFD, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	switch {
	case err == nil:
		return os.NewFile(uintptr(pidfd), "pidfd"), nil
	case errors.Is(err, unix.ENOPROTOOPT) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return nil, ErrNoPidfd
	default:
		return nil, fmt.Errorf("SO_PEERPIDFD: %w", err)
	}
}

// Open returns a pidfd of pid (pidfd_open). The caller closes it. A pid
// learned from a source that can be outrun by recycling (a D-Bus
// GetConnectionUnixProcessID reply) must be confirmed against the source
// again after Open, so the pidfd names the same process.
func Open(pid int) (*os.File, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("pidfd_open(%d): %w", pid, err)
	}
	return os.NewFile(uintptr(fd), "pidfd"), nil
}

// ExeOfPidfd returns the executable and the pid of the process behind pidfd:
//
//  1. The pid is read from /proc/self/fdinfo/<pidfd>.
//  2. /proc/<pid>/exe is read.
//  3. pidfd_send_signal(pidfd, 0) then proves the process still exists. If it
//     had exited before the read, the pid could have named someone else, and
//     the answer is refused.
//
// A deleted executable is ErrExeDeleted. The caller keeps pidfd open (and
// alive, runtime.KeepAlive if it holds an *os.File) during the call.
func ExeOfPidfd(pidfd int) (string, int, error) {
	pid, err := pidOfPidfd(pidfd)
	if err != nil {
		return "", 0, err
	}
	exe, err := readExe(pid)
	// After the read: a process that is gone may have let its pid be reused.
	if alive := unix.PidfdSendSignal(pidfd, 0, nil, 0); alive != nil {
		return "", pid, fmt.Errorf("peer exited: %w", alive)
	}
	return exe, pid, err
}

// pidOfPidfd reads the Pid: line of /proc/self/fdinfo/<pidfd>: -1 once the
// process has exited, 0 when it is outside this pid namespace.
func pidOfPidfd(pidfd int) (int, error) {
	f, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(pidfd))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || pid <= 0 {
				return 0, fmt.Errorf("peer pid not visible (%q)", strings.TrimSpace(v))
			}
			return pid, nil
		}
	}
	return 0, errors.New("pidfd fdinfo has no Pid")
}

// readExe reads /proc/<pid>/exe and refuses a deleted executable.
func readExe(pid int) (string, error) {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(exe, " (deleted)") {
		return "", ErrExeDeleted
	}
	return exe, nil
}
