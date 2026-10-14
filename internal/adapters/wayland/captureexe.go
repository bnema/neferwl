package wayland

import (
	"os"
	"runtime"

	"github.com/bnema/neferwl/internal/adapters/peerexe"
)

// peerExe names the executable behind a client connection, in two steps so a
// caller can keep the pidfd and resolve the executable again at each
// decision.
type peerExe interface {
	// Pidfd returns a pidfd of the peer of the connected unix socket sockFD
	// (SO_PEERPIDFD). It pins the peer process: its pid cannot be recycled
	// while the file is open. peerexe.ErrNoPidfd means the kernel cannot give
	// one.
	Pidfd(sockFD int) (*os.File, error)
	// Exe returns the path of the executable running as the process behind
	// pidfd, and its pid. A deleted executable, or a process that has
	// exited, is an error.
	Exe(pidfd *os.File) (exe string, pid int, err error)
}

// linuxPeerExe resolves the peer through a pidfd (SO_PEERPIDFD, Linux 6.5+),
// with the proof of peerexe.ExeOfPidfd. There is no fallback to the
// SO_PEERCRED pid: it has no such proof, so a kernel without SO_PEERPIDFD
// identifies nobody.
type linuxPeerExe struct {
	// noPidfd simulates a kernel without SO_PEERPIDFD (tests).
	noPidfd bool
}

func (l linuxPeerExe) Pidfd(sockFD int) (*os.File, error) {
	if l.noPidfd {
		return nil, peerexe.ErrNoPidfd
	}
	return peerexe.Pidfd(sockFD)
}

func (linuxPeerExe) Exe(pidfd *os.File) (string, int, error) {
	defer runtime.KeepAlive(pidfd)
	return peerexe.ExeOfPidfd(int(pidfd.Fd()))
}
