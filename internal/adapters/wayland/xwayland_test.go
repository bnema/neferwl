package wayland

import (
	"errors"
	"testing"

	"bnema.dev/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

func xwaylandPair(t *testing.T) *[2]int {
	t.Helper()
	p, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p[1] >= 0 {
			unix.Close(p[1])
		}
	})
	return &p
}

func assertFDClosed(t *testing.T, fd int) {
	t.Helper()
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("fd %d still open: %v", fd, err)
	}
}

func TestAttachXwaylandLifecycle(t *testing.T) {
	h := newAdmissionHarness(t, 1)
	wl := xwaylandPair(t)
	h.s.AttachXwayland(wl[0])
	var first server.Client
	h.s.display.Do(func() { first = h.s.xwayland.client })
	if first == (server.Client{}) {
		t.Fatal("client not recorded")
	}
	wl2 := xwaylandPair(t)
	h.s.AttachXwayland(wl2[0])
	unix.Close(wl[1])
	wl[1] = -1 // Old client's destroy must not clear the new record.
	waitTrue(t, "old client destroyed", func() bool {
		_, err := unix.FcntlInt(uintptr(wl[0]), unix.F_GETFD, 0)
		return errors.Is(err, unix.EBADF)
	})
	h.s.display.Do(func() {
		if h.s.xwayland.client == (server.Client{}) || h.s.xwayland.client == first {
			t.Error("replacement lost")
		}
	})
	unix.Close(wl2[1])
	wl2[1] = -1
	waitTrue(t, "Xwayland disconnected", func() bool {
		clear := false
		h.s.display.Do(func() { clear = h.s.xwayland.client == (server.Client{}) })
		return clear
	})
}

func TestAttachXwaylandFailureClosesFDs(t *testing.T) {
	h := newAdmissionHarness(t, 1)
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[1])
	h.s.AttachXwayland(p[0])
	assertFDClosed(t, p[0])
}

func TestAttachXwaylandStopped(t *testing.T) {
	d, err := server.NewDisplay()
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	s := &Server{display: d}
	wl := xwaylandPair(t)
	s.AttachXwayland(wl[0])
	assertFDClosed(t, wl[0])
}
