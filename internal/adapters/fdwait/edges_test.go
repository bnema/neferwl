package fdwait

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A pinned fd is reported like a transient one, costs no epoll call per
// Poll, and a ready pinned fd a Poll does not list fails it instead of
// spinning.
func TestPollPinned(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pinned, other := eventfd(t), eventfd(t)
	if err := p.Pin(int32(pinned)); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(pinned), Events: unix.POLLIN}}
	if n, err := p.Poll(fds, 1); n != 0 || err != nil {
		t.Fatalf("idle: %d %v", n, err)
	}
	go unix.Write(pinned, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("pinned ready: %d %v %+v", n, err, fds)
	}
	// Still registered after Poll returns: it is pinned.
	if err := p.raw.Control(func(ep uintptr) {
		var events [2]unix.EpollEvent
		if n, err := unix.EpollWait(int(ep), events[:], 0); n != 1 || err != nil || events[0].Fd != int32(pinned) {
			t.Errorf("pinned registration: %d %v %+v", n, err, events[:n])
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll([]unix.PollFd{{Fd: int32(other), Events: unix.POLLIN}}, 1000); err == nil {
		t.Fatal("Poll without a ready pinned fd succeeded")
	}
}

// Polling only pinned fds allocates nothing and makes no epoll_ctl call.
func TestPollPinnedAllocations(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd := eventfd(t)
	if err := p.Pin(int32(fd)); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	signal(t, fd)
	p.Poll(fds, -1)
	if allocs := testing.AllocsPerRun(100, func() { p.Poll(fds, -1) }); allocs != 0 {
		t.Errorf("pinned ready Poll: %.1f allocs, want 0", allocs)
	}
}

// Wake ends a wait without a timeout and returns no events; wakes before a
// Poll coalesce into one early return.
func TestPollWake(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fds := []unix.PollFd{{Fd: int32(eventfd(t)), Events: unix.POLLIN}}
	done := make(chan error, 1)
	go func() {
		n, err := p.Poll(fds, -1)
		if err == nil && n != 0 {
			err = errors.New("woken poll reported events")
		}
		done <- err
	}()
	p.Wake()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wake did not end the wait")
	}
	p.Wake()
	p.Wake()
	if n, err := p.Poll(fds, 1000); n != 0 || err != nil {
		t.Fatalf("coalesced wake: %d %v", n, err)
	}
	if n, err := p.Poll(fds, 1); n != 0 || err != nil {
		t.Fatalf("second wake was not coalesced: %d %v", n, err)
	}
}

// After the StopOn context ends, a running and every later Poll return at
// once, also with no timeout.
func TestPollStopOn(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p.StopOn(ctx)
	fds := []unix.PollFd{{Fd: int32(eventfd(t)), Events: unix.POLLIN}}
	done := make(chan error, 1)
	go func() { _, err := p.Poll(fds, -1); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
	for range 3 {
		if n, err := p.Poll(fds, -1); n != 0 || err != nil {
			t.Fatalf("poll after stop: %d %v", n, err)
		}
	}
}

func eventfd(t *testing.T) int {
	t.Helper()
	fd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return fd
}

func signal(t *testing.T, fd int) {
	t.Helper()
	if _, err := unix.Write(fd, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
}

// assertNoWatches fails if Poll left a registration behind, by making every
// test descriptor readable and checking epoll reports nothing.
func assertNoWatches(t *testing.T, p *Poller, fds ...int) {
	t.Helper()
	for _, fd := range fds {
		signal(t, fd)
	}
	if err := p.raw.Control(func(ep uintptr) {
		var events [4]unix.EpollEvent
		if n, err := unix.EpollWait(int(ep), events[:], 0); n != 0 || err != nil {
			t.Errorf("registrations left after Poll returned: %d %v", n, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

// Poll(fds, 0) reports existing readiness like poll(2): an expired netpoll
// deadline never runs the readiness check.
func TestPollZeroTimeoutReportsReady(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd := eventfd(t)
	fds := []unix.PollFd{{Fd: -1}, {Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := p.Poll(fds, 0); n != 0 || err != nil {
		t.Fatalf("idle: %d %v", n, err)
	}
	signal(t, fd)
	if n, err := p.Poll(fds, 0); n != 1 || err != nil || fds[1].Revents&unix.POLLIN == 0 {
		t.Fatalf("ready: %d %v %+v", n, err, fds)
	}
}

// A descriptor listed twice is watched once and removed once.
func TestPollDuplicateDescriptor(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd := eventfd(t)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := p.Poll(fds, 1); n != 0 || err != nil {
		t.Fatalf("duplicate: %d %v", n, err)
	}
	assertNoWatches(t, p, fd)
}

// A failed registration removes the ones already added in the same call.
func TestPollFailedWatchRemovesAdded(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd := eventfd(t)
	var self int32
	if err := p.raw.Control(func(ep uintptr) { self = int32(ep) }); err != nil {
		t.Fatal(err)
	}
	// epoll refuses to watch itself (EINVAL) and is not ready.
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: self, Events: unix.POLLIN}}
	if _, err := p.Poll(fds, 1); err == nil {
		t.Fatal("watching the epoll fd itself succeeded")
	}
	assertNoWatches(t, p, fd)
}

// A registration whose fd was closed while another reference keeps its file
// open cannot be removed: Poll fails from then on instead of spinning.
func TestPollFailsAfterLostRegistration(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := unix.Dup(fd)
	if err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	defer unix.Close(dup)
	if err := p.raw.Control(func(ep uintptr) {
		ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)}
		if err := unix.EpollCtl(int(ep), unix.EPOLL_CTL_ADD, fd, &ev); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	unix.Close(fd) // what a caller closing fd during a wait does
	p.fds, p.added = []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 1
	p.unwatch()
	p.fds = nil
	other := eventfd(t)
	if _, err := p.Poll([]unix.PollFd{{Fd: int32(other), Events: unix.POLLIN}}, 1); err == nil {
		t.Fatal("Poll succeeded with a registration it could not remove")
	}
}
