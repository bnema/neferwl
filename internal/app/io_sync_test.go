package app

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/wlturbo"

	"golang.org/x/sys/unix"
)

func msUntil(end time.Time) int { return int(time.Until(end).Milliseconds()) }

// fileChangesWithin observes real filesystem publication, including atomic
// replacement. Install the watch before checking the file to avoid losing a
// publication. The returned wait reports false once budget has passed since
// the call (zero means 10 s per wait).
func fileChangesWithin(t *testing.T, dir string, budget time.Duration) func() bool {
	t.Helper()
	end := time.Now().Add(budget)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO|unix.IN_CREATE); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		t.Helper()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			ms := 10000
			if budget > 0 {
				ms = msUntil(end)
				if ms <= 0 {
					return false
				}
			}
			n, err := unix.Poll(fds, ms)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				return false
			}
			var buf [16384]byte
			if _, err := unix.Read(fd, buf[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
				t.Fatal(err)
			}
			return true
		}
	}
}

// waitProcessOutputUntil waits for new process output; false once timeout fires.
func waitProcessOutputUntil(b *lockedBuffer, timeout <-chan time.Time) bool {
	select {
	case <-b.changed:
		return true
	case <-timeout:
		return false
	}
}

// dispatchUntil dispatches wire events on c until ready, bounded by one timer that closes
// the connection; detail describes the state on timeout.
//
//testpolicy:realtime blocking Dispatch on the real Wayland socket against the headless compositor; the bounding timer closes the connection
func dispatchUntil(t *testing.T, c *wlturbo.Display, d time.Duration, detail func() string, ready func() bool) {
	t.Helper()
	var expired atomic.Bool
	timer := time.AfterFunc(d, func() { expired.Store(true); _ = c.Close() })
	defer timer.Stop()
	for !ready() {
		if err := c.Dispatch(); err != nil {
			if expired.Load() {
				t.Fatalf("timed out after %v: %s", d, detail())
			}
			t.Fatal(err)
		}
	}
}
