package fdwait

import (
	"golang.org/x/sys/unix"
	"testing"
)

// Closing a watched file removes it from epoll. Reusing its number must
// register the new file, not leave it waiting on the old registration.
func TestPollRecycledDescriptor(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if _, err := p.Poll(fds, 1); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	unix.Close(fd)
	replacement, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(replacement)
	if replacement != fd {
		t.Skip("descriptor number not reused")
	}
	if _, err := unix.Write(replacement, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil {
		t.Fatalf("reused descriptor: %d %v", n, err)
	}
}

func TestPollHangup(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	unix.Close(pipe[1])
	fds := []unix.PollFd{{Fd: int32(pipe[0]), Events: unix.POLLIN}}
	if n, err := p.Poll(fds, 1000); n != 1 || err != nil || fds[0].Revents&unix.POLLHUP == 0 {
		t.Fatalf("hangup: %d %v %+v", n, err, fds)
	}
}
