package fdwait

import (
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

// Epoll retains a registration after its fd closes if another descriptor
// still refers to the same open file description.
func TestRemovedDuplicatedDescriptorDoesNotKeepPollReadable(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	old, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := unix.Dup(old)
	if err != nil {
		unix.Close(old)
		t.Fatal(err)
	}
	defer unix.Close(dup)
	if _, err := p.Poll([]unix.PollFd{{Fd: int32(old), Events: unix.POLLIN}}, 1); err != nil {
		t.Fatal(err)
	}
	// Create the next descriptor before closing old so its number is not
	// reused. This leaves an old epoll registration with no usable fd.
	fresh, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fresh)
	unix.Close(old)
	if _, err := unix.Write(dup, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := p.raw.Control(func(ep uintptr) {
		var events [1]unix.EpollEvent
		if n, err := unix.EpollWait(int(ep), events[:], 0); n != 0 || err != nil {
			t.Errorf("closed watched descriptor still makes epoll readable: %d %v", n, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.Poll([]unix.PollFd{{Fd: int32(fresh), Events: unix.POLLIN}}, 20); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		p.Close()
		t.Fatal("stale epoll readiness prevents timeout")
	}
}
