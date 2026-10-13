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
	file *os.File
	raw  syscall.RawConn
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
	return &Poller{file: f, raw: raw}, nil
}
func (p *Poller) Close() error { return p.file.Close() }

// Poll waits for readable/error events. It never consumes caller events.
// Each call explicitly removes its epoll registrations before the caller can
// close or recycle watched fds. Relying on close to remove them is unsafe:
// another fd or a kernel fence may retain the open file description.
func (p *Poller) Poll(fds []unix.PollFd, timeout int) (int, error) {
	var ctlErr error
	added := 0
	err := p.raw.Control(func(ep uintptr) {
		for _, f := range fds {
			if f.Fd < 0 {
				added++
				continue
			}
			ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: f.Fd}
			if e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_ADD, int(f.Fd), &ev); e != nil {
				ctlErr = e
				return
			}
			added++
		}
	})
	defer p.raw.Control(func(ep uintptr) {
		for _, f := range fds[:added] {
			if f.Fd >= 0 {
				unix.EpollCtl(int(ep), unix.EPOLL_CTL_DEL, int(f.Fd), nil)
			}
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
