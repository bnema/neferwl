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

func TestOpenDeviceGrabFailureKeepsFD(t *testing.T) {
	d := newMockevdev(t)
	d.EXPECT().open("/dev/input/event3").Return(7, nil)
	d.EXPECT().grab(7).Return(unix.EBUSY)
	var out lockedBuffer
	fd, err := newSeat(testLog(&out), d).OpenDevice("/dev/input/event3")
	require.NoError(t, err)
	assert.Equal(t, 7, fd)
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

// A regular file is not an evdev node: the grab fails with ENOTTY and the
// fd must still be returned. Never opens a real /dev/input device.
func TestRealFileGrabENOTTYTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event0")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	var out lockedBuffer
	s := New(testLog(&out))
	fd, err := s.OpenDevice(path)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, fd, 0)
	assert.Contains(t, out.String(), "inappropriate ioctl")
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.Equal(t, unix.O_RDWR, flags&unix.O_ACCMODE)
	assert.NotZero(t, flags&unix.O_NONBLOCK)
	s.CloseDevice(fd)
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.ErrorIs(t, err, unix.EBADF)
}
