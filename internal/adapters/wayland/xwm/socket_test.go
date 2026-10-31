package xwm

import (
	"golang.org/x/sys/unix"
	"testing"
)

func waitSocket(t *testing.T, fd int, events int16) {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
	for {
		n, err := unix.Poll(fds, 5000)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			t.Fatalf("socket readiness: %d %v", n, err)
		}
		return
	}
}
