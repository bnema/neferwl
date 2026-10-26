package app

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fileChanges observes real filesystem publication, including atomic replacement.
// Install the watch before checking the file to avoid losing a publication.
func fileChanges(t *testing.T, dir string) func() {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO|unix.IN_CREATE); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			n, err := unix.Poll(fds, 10000)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				t.Fatal("timed out waiting for filesystem publication")
			}
			var buf [16384]byte
			if _, err := unix.Read(fd, buf[:]); err != nil && err != unix.EAGAIN {
				t.Fatal(err)
			}
			return
		}
	}
}

//testpolicy:realtime waits for bytes written by a real child process
func waitProcessOutput(t *testing.T, b *lockedBuffer) {
	t.Helper()
	select {
	case <-b.changed:
	case <-time.After(15 * time.Second):
		t.Fatalf("no process output: %s", b.String())
	}
}
