package directseat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLog(out *lockedBuffer) zerowrap.Logger {
	return zerowrap.Logger{Logger: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: out}).With().Str("component", "seat").Logger()}
}

func TestOpenDeviceGrabs(t *testing.T) {
	d := newMockevdev(t)
	d.EXPECT().open("/dev/input/event3").Return(7, nil)
	d.EXPECT().grab(7).Return(nil)
	var out lockedBuffer
	fd, err := newSeat(testLog(&out), d).OpenDevice("/dev/input/event3")
	require.NoError(t, err)
	assert.Equal(t, 7, fd)
	assert.NotContains(t, out.String(), `"warn"`)
}

func TestOpenDeviceGrabBusyRefused(t *testing.T) {
	d := newMockevdev(t)
	d.EXPECT().open("/dev/input/event3").Return(7, nil)
	d.EXPECT().grab(7).Return(unix.EBUSY)
	d.EXPECT().close(7).Return(nil).Once()
	var out lockedBuffer
	fd, err := newSeat(testLog(&out), d).OpenDevice("/dev/input/event3")
	require.ErrorIs(t, err, unix.EBUSY)
	assert.Contains(t, err.Error(), "another client holds an exclusive grab")
	assert.Contains(t, err.Error(), "/dev/input/event3")
	assert.Equal(t, -1, fd)
	assert.Contains(t, out.String(), `"level":"warn"`)
	assert.Contains(t, out.String(), `"component":"seat"`)
}

func TestOpenDeviceOpenFailure(t *testing.T) {
	d := newMockevdev(t)
	d.EXPECT().open("/dev/input/event3").Return(-1, unix.EACCES)
	_, err := newSeat(testLog(&lockedBuffer{}), d).OpenDevice("/dev/input/event3")
	require.ErrorIs(t, err, unix.EACCES)
}

func TestCloseDeviceReleasesAndCloses(t *testing.T) {
	d := newMockevdev(t)
	var order []string
	d.EXPECT().ungrab(7).RunAndReturn(func(int) error { order = append(order, "ungrab"); return nil })
	d.EXPECT().close(7).RunAndReturn(func(int) error { order = append(order, "close"); return nil })
	newSeat(testLog(&lockedBuffer{}), d).CloseDevice(7)
	assert.Equal(t, []string{"ungrab", "close"}, order)
}

func TestCloseDeviceClosesAfterUngrabFailure(t *testing.T) {
	d := newMockevdev(t)
	d.EXPECT().ungrab(7).Return(errors.New("not grabbed"))
	d.EXPECT().close(7).Return(nil)
	newSeat(testLog(&lockedBuffer{}), d).CloseDevice(7)
}

func TestSeatIdentityAndActive(t *testing.T) {
	var out lockedBuffer
	s := newSeat(testLog(&out), newMockevdev(t))
	assert.Equal(t, "seat0", s.Name())
	assert.True(t, <-s.Active())
	s.SwitchVT(2)
	assert.Contains(t, out.String(), `"level":"debug"`)
}

// A regular file is not an evdev node: the grab fails with ENOTTY, the
// device is refused and the fd it opened is closed. Never opens a real
// /dev/input device.
func TestRealFileGrabENOTTYRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event0")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	before := openFDs(t)
	var out lockedBuffer
	fd, err := New(testLog(&out)).OpenDevice(path)
	require.ErrorIs(t, err, unix.ENOTTY)
	assert.Equal(t, -1, fd)
	assert.Contains(t, out.String(), "inappropriate ioctl")
	assert.Equal(t, before, openFDs(t), "the refused device's fd leaked")
}

// openFDs lists the process's open descriptors that point at regular files
// named event0 (the test's fake device).
func openFDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	var fds []string
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && filepath.Base(target) == "event0" {
			fds = append(fds, e.Name())
		}
	}
	return fds
}

// The kernel boundary opens devices read-write, non-blocking and close-on-exec.
func TestUnixOpenFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event0")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	fd, err := unixEvdev{}.open(path)
	require.NoError(t, err)
	defer func() { _ = unix.Close(fd) }()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.Equal(t, unix.O_RDWR, flags&unix.O_ACCMODE)
	assert.NotZero(t, flags&unix.O_NONBLOCK)
	fdflags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, fdflags&unix.FD_CLOEXEC)
}
