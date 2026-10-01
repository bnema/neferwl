package wayland

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// peerExe names the executable behind a client connection, in two steps so a
// caller can keep the pidfd and resolve the executable again at each
// decision.
type peerExe interface {
	// Pidfd returns a pidfd of the peer of the connected unix socket sockFD
	// (SO_PEERPIDFD). It pins the peer process: its pid cannot be recycled
	// while the file is open. errNoPidfd means the kernel cannot give one.
	Pidfd(sockFD int) (*os.File, error)
	// Exe returns the path of the executable running as the process behind
	// pidfd, and its pid. A deleted executable, or a process that has
	// exited, is an error.
	Exe(pidfd *os.File) (exe string, pid int, err error)
}

var (
	errExeDeleted = errors.New("executable deleted")
	// errNoPidfd is a kernel without SO_PEERPIDFD (before Linux 6.5): the
	// peer cannot be identified safely.
	errNoPidfd = errors.New("kernel has no SO_PEERPIDFD")
)

// linuxPeerExe resolves the peer through a pidfd (SO_PEERPIDFD, Linux 6.5+):
//
//  1. The pidfd pins the peer process: its pid cannot be recycled while the
//     descriptor is open. The pid is read from /proc/self/fdinfo/<pidfd>.
//  2. /proc/<pid>/exe is read.
//  3. pidfd_send_signal(pidfd, 0) then proves the process still exists. If it
//     had exited before the read, the pid could have named someone else, and
//     the answer is refused.
//
// There is no fallback to the SO_PEERCRED pid: it has no such proof, so a
// kernel without SO_PEERPIDFD identifies nobody.
type linuxPeerExe struct {
	// noPidfd simulates a kernel without SO_PEERPIDFD (tests).
	noPidfd bool
}

func (l linuxPeerExe) Pidfd(sockFD int) (*os.File, error) {
	if l.noPidfd {
		return nil, errNoPidfd
	}
	pidfd, err := unix.GetsockoptInt(sockFD, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	switch {
	case err == nil:
		return os.NewFile(uintptr(pidfd), "pidfd"), nil
	case errors.Is(err, unix.ENOPROTOOPT) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return nil, errNoPidfd
	default:
		return nil, fmt.Errorf("SO_PEERPIDFD: %w", err)
	}
}

func (linuxPeerExe) Exe(pidfd *os.File) (string, int, error) {
	defer runtime.KeepAlive(pidfd)
	return exeOfPidfd(int(pidfd.Fd()))
}

// exeOfPidfd runs the pidfd procedure above.
func exeOfPidfd(pidfd int) (string, int, error) {
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
		return "", errExeDeleted
	}
	return exe, nil
}
