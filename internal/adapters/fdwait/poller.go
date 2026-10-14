// Package fdwait parks descriptor waits in Go's netpoller.
package fdwait

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Poller owns an epoll descriptor and is used by one goroutine; Close may
// also run from another goroutine to end a wait. Watched descriptors remain
// owned by the caller, who must not close them while a Poll waits.
type Poller struct {
	file *os.File
	raw  syscall.RawConn
	// broken is set when a registration could not be removed: it may keep
	// the epoll descriptor readable forever, so every later Poll fails
	// instead of spinning.
	broken error
}

func New() (*Poller, error) {
	fd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("fdwait epoll create: %w", err)
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("fdwait epoll nonblock: %w", err)
	}
	f := os.NewFile(uintptr(fd), "fdwait")
	raw, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("fdwait epoll conn: %w", err)
	}
	return &Poller{file: f, raw: raw}, nil
}

func (p *Poller) Close() error { return p.file.Close() }

// Poll waits like poll(2) for readable/error events on fds and fills their
// Revents. It never consumes caller events. Negative fds are ignored and a
// descriptor listed twice is watched once. timeout is in milliseconds; a
// negative one waits until an event or Close.
//
// Each call explicitly removes its epoll registrations before returning.
// Relying on close to remove them is unsafe: another fd or a kernel fence
// may retain the open file description and keep the epoll fd readable.
// When nothing is ready yet, a descriptor epoll cannot watch (EPERM,
// EINVAL, ELOOP for epoll nested too deep) fails the call.
func (p *Poller) Poll(fds []unix.PollFd, timeout int) (int, error) {
	if p.broken != nil {
		return 0, p.broken
	}
	// Ready descriptors need no registration, and an expired deadline
	// would skip the readiness check in RawConn.Read.
	if n, err := pollNow(fds); n != 0 || err != nil || timeout == 0 {
		return n, err
	}
	var ctlErr error
	added := 0
	err := p.raw.Control(func(ep uintptr) {
		for _, f := range fds {
			if f.Fd >= 0 {
				ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: f.Fd}
				e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_ADD, int(f.Fd), &ev)
				if e != nil && !(errors.Is(e, unix.EEXIST) && listedBefore(fds, added)) {
					ctlErr = fmt.Errorf("fdwait watch fd %d: %w", f.Fd, e)
					return
				}
			}
			added++
		}
	})
	defer p.unwatch(fds[:added])
	if err != nil {
		return 0, err
	}
	if ctlErr != nil {
		return 0, ctlErr
	}
	if timeout > 0 {
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
		n, pollErr = pollNow(fds)
		return n != 0 || pollErr != nil
	})
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return n, pollErr
}

// unwatch removes the registrations Poll added for fds. A failure means a
// watched fd was closed during the wait: its registration may outlive it.
func (p *Poller) unwatch(fds []unix.PollFd) {
	p.raw.Control(func(ep uintptr) {
		for i, f := range fds {
			if f.Fd < 0 || listedBefore(fds, i) {
				continue
			}
			if e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_DEL, int(f.Fd), nil); e != nil && p.broken == nil {
				p.broken = fmt.Errorf("fdwait unwatch fd %d (closed during a wait?): %w", f.Fd, e)
			}
		}
	})
}

// listedBefore reports whether fds[i].Fd also appears earlier in fds.
func listedBefore(fds []unix.PollFd, i int) bool {
	fd := fds[i].Fd
	return slices.ContainsFunc(fds[:i], func(f unix.PollFd) bool { return f.Fd == fd })
}

// pollNow checks fds without blocking, retrying an interrupted check.
func pollNow(fds []unix.PollFd) (int, error) {
	for {
		n, err := unix.Poll(fds, 0)
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}
