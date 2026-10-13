// Package fdwait parks descriptor waits in Go's netpoller.
package fdwait

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Poller owns an epoll descriptor and is used by one goroutine.
// Watched descriptors remain owned by the caller.
type Poller struct {
	file       *os.File
	raw        syscall.RawConn
	registered map[int]struct{}
}

func New() (*Poller, error) {
	fd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "fdwait")
	raw, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Poller{file: f, raw: raw, registered: make(map[int]struct{})}, nil
}

func (p *Poller) Close() error { return p.file.Close() }

// Poll has the readable/error semantics of unix.Poll. A negative timeout
// waits indefinitely. Timeout expiration returns zero, not an error.
func (p *Poller) Poll(fds []unix.PollFd, timeout int) (int, error) {
	// Remove registrations no longer requested. Closing a watched fd already
	// removes it from epoll, so ENOENT and EBADF are harmless here.
	err := p.raw.Control(func(ep uintptr) {
		for fd := range p.registered {
			found := false
			for _, f := range fds {
				if int(f.Fd) == fd {
					found = true
					break
				}
			}
			if !found {
				unix.EpollCtl(int(ep), unix.EPOLL_CTL_DEL, fd, nil)
				delete(p.registered, fd)
			}
		}
	})
	if err != nil {
		return 0, err
	}
	var ctlErr error
	err = p.raw.Control(func(ep uintptr) {
		for _, f := range fds {
			fd := int(f.Fd)
			if fd < 0 {
				continue
			}
			// MOD detects a descriptor number recycled after its old file closed.
			ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)}
			e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_MOD, fd, &ev)
			if errors.Is(e, unix.ENOENT) {
				e = unix.EpollCtl(int(ep), unix.EPOLL_CTL_ADD, fd, &ev)
			}
			if e != nil {
				ctlErr = e
				return
			}
			p.registered[fd] = struct{}{}
		}
	})
	if err != nil {
		return 0, err
	}
	if ctlErr != nil {
		return 0, ctlErr
	}
	if timeout >= 0 {
		err = p.file.SetReadDeadline(time.Now().Add(time.Duration(timeout) * time.Millisecond))
	} else {
		err = p.file.SetReadDeadline(time.Time{})
	}
	if err != nil {
		return 0, err
	}
	var n int
	var pollErr error
	err = p.raw.Read(func(_ uintptr) bool {
		n, pollErr = unix.Poll(fds, 0)
		return n > 0 || (pollErr != nil && !errors.Is(pollErr, unix.EINTR))
	})
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return n, pollErr
}
