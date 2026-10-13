package fdwait

import (
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func TestPollReadinessTimeoutAndReplacement(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := p.Poll(fds, 5); n != 0 || err != nil {
		t.Fatalf("timeout: %d %v", n, err)
	}
	done := make(chan error, 1)
	go func() { _, e := unix.Write(fd, []byte{1, 0, 0, 0, 0, 0, 0, 0}); done <- e }()
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("readiness: %d %v %+v", n, err, fds)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Level readiness must remain visible before the caller consumes it.
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil {
		t.Fatalf("level: %d %v", n, err)
	}
	var b [8]byte
	if _, err := unix.Read(fd, b[:]); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Poll(fds, 5); n != 0 || err != nil {
		t.Fatalf("consumed: %d %v", n, err)
	}
	other, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(other)
	fds[0].Fd = int32(other)
	if _, err := unix.Write(other, b[:]); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil {
		t.Fatalf("replacement: %d %v", n, err)
	}
	if _, ok := p.registered[fd]; ok {
		t.Fatal("old registration retained")
	}
}

func TestCloseUnblocksPoll(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	done := make(chan error, 1)
	go func() { _, e := p.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, -1); done <- e }()
	// Close is safe both before and during the netpoll wait.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock wait")
	}
}
