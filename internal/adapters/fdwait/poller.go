// Package fdwait parks descriptor waits in Go's netpoller.
package fdwait

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Poller owns an epoll descriptor and is used by one goroutine; Wake and
// Close may run from any goroutine. Watched descriptors remain owned by the
// caller, who must not close them while a Poll waits.
//
// Descriptors that live as long as the poller are registered once with Pin;
// others are registered for one Poll call. A Poll returns on a watched fd,
// on Wake, once the context given to StopOn ends, or on Close: a loop needs
// no polling timeout to notice work or shutdown.
type Poller struct {
	file *os.File
	raw  syscall.RawConn
	// wake is an eventfd pinned in the epoll set: Wake writes it.
	wake    *os.File
	wakeRaw syscall.RawConn
	wakeFD  int32
	ctx     context.Context
	stop    func() bool
	pinned  []int32 // besides wakeFD
	// broken is set when a registration could not be removed: it may keep
	// the epoll descriptor readable forever, so every later Poll fails
	// instead of spinning.
	broken error
	// noDeadline: the read deadline is already cleared.
	noDeadline bool

	// State of the running Poll, used by the callbacks below. They are
	// bound once in New, so a Poll allocates nothing.
	fds     []unix.PollFd
	check   []unix.PollFd
	added   int
	ctlErr  error
	n       int
	pollErr error
	buf     [8]byte
	addFn   func(uintptr)
	delFn   func(uintptr)
	checkFn func(uintptr)
	readFn  func(uintptr) bool
	drainFn func(uintptr)
	kickFn  func(uintptr)
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
	p := &Poller{file: os.NewFile(uintptr(fd), "fdwait")}
	if p.raw, err = p.file.SyscallConn(); err != nil {
		p.file.Close()
		return nil, fmt.Errorf("fdwait epoll conn: %w", err)
	}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		p.file.Close()
		return nil, fmt.Errorf("fdwait wake eventfd: %w", err)
	}
	p.wake, p.wakeFD = os.NewFile(uintptr(efd), "fdwait-wake"), int32(efd)
	p.addFn, p.delFn, p.readFn, p.checkFn = p.watch, p.unwatchAll, p.ready, p.pollNow
	p.drainFn, p.kickFn = p.drain, p.kick
	if p.wakeRaw, err = p.wake.SyscallConn(); err == nil {
		err = p.control(unix.EPOLL_CTL_ADD, p.wakeFD)
	}
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("fdwait wake: %w", err)
	}
	return p, nil
}

// Close ends a running Poll and releases the epoll and wake descriptors.
func (p *Poller) Close() error {
	if p.stop != nil {
		p.stop()
	}
	err := p.file.Close()
	p.wake.Close()
	return err
}

func (p *Poller) control(op int, fd int32) error {
	var err error
	if cerr := p.raw.Control(func(ep uintptr) {
		ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: fd}
		err = unix.EpollCtl(int(ep), op, int(fd), &ev)
	}); cerr != nil {
		return cerr
	}
	return err
}

// Pin registers fd for every later Poll, which then costs it no epoll call.
// fd must stay open until Close, and every Poll must list it: a ready pinned
// fd that a Poll does not list fails that Poll.
func (p *Poller) Pin(fd int32) error {
	if err := p.control(unix.EPOLL_CTL_ADD, fd); err != nil {
		return fmt.Errorf("fdwait pin fd %d: %w", fd, err)
	}
	p.pinned = append(p.pinned, fd)
	return nil
}

// StopOn makes every Poll return (0, nil) at once after ctx ends. Call it
// once, before polling.
func (p *Poller) StopOn(ctx context.Context) {
	p.ctx = ctx
	p.stop = context.AfterFunc(ctx, p.Wake)
}

// Wake makes the running or next Poll return. Wakes before that Poll
// coalesce. Safe from any goroutine, also after Close.
func (p *Poller) Wake() { _ = p.wakeRaw.Control(p.kickFn) }

func (p *Poller) kick(fd uintptr) {
	one := [8]byte{1}
	_, _ = unix.Write(int(fd), one[:])
}

func (p *Poller) drain(fd uintptr) { _, _ = unix.Read(int(fd), p.buf[:]) }

func (p *Poller) stopped() bool { return p.ctx != nil && p.ctx.Err() != nil }

// Poll waits like poll(2) for readable/error events on fds and fills their
// Revents. It never consumes caller events. Negative fds are ignored and a
// descriptor listed twice is watched once. timeout is in milliseconds; a
// negative one waits until an event, Wake, the end of the StopOn context or
// Close. A Wake or stop returns the events seen, possibly none.
//
// Each call removes the registrations it made before returning. Relying on
// close to remove them is unsafe: another fd or a kernel fence may retain
// the open file description and keep the epoll fd readable. When nothing is
// ready yet, a descriptor epoll cannot watch (EPERM, EINVAL, ELOOP for epoll
// nested too deep) fails the call.
func (p *Poller) Poll(fds []unix.PollFd, timeout int) (int, error) {
	if p.broken != nil {
		return 0, p.broken
	}
	p.fds = fds
	defer func() { p.fds = nil }()
	transient := false
	for i := range fds {
		transient = transient || !p.skip(i)
	}
	// An expired deadline skips RawConn.Read's own check, and a ready
	// transient fd needs no registration. Pinned fds are checked by Read.
	if (timeout == 0 || transient) && p.ready(0) || timeout == 0 {
		return p.n, p.pollErr
	}
	p.added, p.ctlErr = 0, nil
	if transient {
		err := p.raw.Control(p.addFn)
		defer p.unwatch()
		if err != nil {
			return 0, err
		}
		if p.ctlErr != nil {
			return 0, p.ctlErr
		}
	}
	var err error
	switch {
	case timeout > 0:
		err, p.noDeadline = p.file.SetReadDeadline(time.Now().Add(time.Duration(timeout)*time.Millisecond)), false
	case !p.noDeadline:
		err, p.noDeadline = p.file.SetReadDeadline(time.Time{}), true
	}
	if err != nil {
		return 0, err
	}
	err = p.raw.Read(p.readFn)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return p.n, p.pollErr
}

// ready checks the caller's fds, the wake fd and unlisted pinned fds without
// blocking, retrying an interrupted check. It reports whether Poll returns.
func (p *Poller) ready(uintptr) bool {
	p.check = append(append(p.check[:0], p.fds...), unix.PollFd{Fd: p.wakeFD, Events: unix.POLLIN})
	for _, fd := range p.pinned {
		if !slices.ContainsFunc(p.fds, func(f unix.PollFd) bool { return f.Fd == fd }) {
			p.check = append(p.check, unix.PollFd{Fd: fd, Events: unix.POLLIN})
		}
	}
	// Under the wake file's reference, so a concurrent Close cannot free
	// its number for reuse during the check.
	if p.wakeRaw.Control(p.checkFn) != nil {
		p.n, p.pollErr = 0, os.ErrClosed
	}
	if p.pollErr != nil {
		p.n = 0
		return true
	}
	for i := range p.fds {
		p.fds[i].Revents = p.check[i].Revents
	}
	for _, f := range p.check[len(p.fds)+1:] {
		if f.Revents != 0 {
			// Its readiness keeps the epoll fd readable: waiting would spin.
			p.n, p.pollErr = 0, fmt.Errorf("fdwait: pinned fd %d is ready but not polled", f.Fd)
			return true
		}
	}
	woke := p.check[len(p.fds)].Revents != 0
	if woke {
		p.n--
		// A stopped poller stays awake: every later Poll returns at once.
		if !p.stopped() {
			_ = p.wakeRaw.Control(p.drainFn)
			if p.stopped() { // the stop's write may have been drained
				p.Wake()
			}
		}
	}
	return p.n != 0 || woke
}

func (p *Poller) pollNow(uintptr) {
	for {
		p.n, p.pollErr = unix.Poll(p.check, 0)
		if !errors.Is(p.pollErr, unix.EINTR) {
			return
		}
	}
}

// skip reports whether fds[i] needs no registration of its own.
func (p *Poller) skip(i int) bool {
	fd := p.fds[i].Fd
	return fd < 0 || slices.Contains(p.pinned, fd) || listedBefore(p.fds, i)
}

func (p *Poller) watch(ep uintptr) {
	for i, f := range p.fds {
		if !p.skip(i) {
			ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: f.Fd}
			if e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_ADD, int(f.Fd), &ev); e != nil {
				p.ctlErr = fmt.Errorf("fdwait watch fd %d: %w", f.Fd, e)
				return
			}
		}
		p.added++
	}
}

// unwatch removes the registrations Poll added. A failure means a watched
// fd was closed during the wait: its registration may outlive it.
func (p *Poller) unwatch() {
	if p.added > 0 {
		_ = p.raw.Control(p.delFn)
	}
}

func (p *Poller) unwatchAll(ep uintptr) {
	for i, f := range p.fds[:p.added] {
		if p.skip(i) {
			continue
		}
		if e := unix.EpollCtl(int(ep), unix.EPOLL_CTL_DEL, int(f.Fd), nil); e != nil && p.broken == nil {
			p.broken = fmt.Errorf("fdwait unwatch fd %d (closed during a wait?): %w", f.Fd, e)
		}
	}
}

// listedBefore reports whether fds[i].Fd also appears earlier in fds.
func listedBefore(fds []unix.PollFd, i int) bool {
	fd := fds[i].Fd
	return slices.ContainsFunc(fds[:i], func(f unix.PollFd) bool { return f.Fd == fd })
}
