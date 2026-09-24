package drm

import (
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/mock"
)

// The ioctl numbers encode the struct size; they must match the kernel ABI.
func TestCursorABI(t *testing.T) {
	if s := unsafe.Sizeof(modeCursor2{}); s != 36 || ioctlCursor2>>16&0x3fff != 36 {
		t.Fatalf("drm_mode_cursor2 size %d", s)
	}
	if s := unsafe.Sizeof(getCap{}); s != 16 || ioctlGetCap>>16&0x3fff != 16 {
		t.Fatalf("drm_get_cap size %d", s)
	}
	if s := unsafe.Sizeof(waitVblank{}); s != 24 || ioctlVblank>>16&0x3fff != 24 {
		t.Fatalf("drm_wait_vblank size %d", s)
	}
}

// testCursor returns a shown cursor on a mocked plane; the worker is started.
func testCursor(t *testing.T) (*Cursor, *mockcursorPlane) {
	plane := newMockcursorPlane(t)
	c := &Cursor{fd: -1, buf: &dumbBuffer{}, size: 64, shown: true, plane: plane}
	return c, plane
}

// The cursor moves once per vblank, to the latest position: moving it during
// scanout tears it into doubled images.
func TestCursorMovesOncePerVblank(t *testing.T) {
	c, plane := testCursor(t)
	vblank := make(chan struct{})
	moved := make(chan modeCursor2, 16)
	plane.EXPECT().WaitVblank().RunAndReturn(func() error { <-vblank; return nil })
	plane.EXPECT().Set(mock.Anything).RunAndReturn(func(v *modeCursor2) error { moved <- *v; return nil })
	c.start()
	defer func() { close(vblank); c.close() }()
	for i := range 100 {
		c.Move(float64(i), 0)
	}
	select {
	case v := <-moved:
		t.Fatalf("moved to %d before vblank", v.x)
	case <-time.After(20 * time.Millisecond):
	}
	vblank <- struct{}{}
	if v := <-moved; v.x != 99 || v.flags != cursorMove {
		t.Fatalf("applied %+v, want a move to the latest x=99", v)
	}
	select {
	case v := <-moved:
		t.Fatalf("extra move to %d without new input", v.x)
	case <-time.After(20 * time.Millisecond):
	}
}

// Move must never wait for the plane, or libinput stops reading and the
// kernel drops mouse reports.
func TestCursorMoveDoesNotBlockOnPlane(t *testing.T) {
	c, plane := testCursor(t)
	release := make(chan struct{})
	plane.EXPECT().WaitVblank().RunAndReturn(func() error { <-release; return nil })
	plane.EXPECT().Set(mock.Anything).Return(nil).Maybe()
	c.start()
	defer func() { close(release); c.close() }()
	for i := range 1000 { // the worker is stuck in WaitVblank the whole time
		c.Move(float64(i), 0)
	}
	if s := c.TakeStats(); s.Moves != 1000 {
		t.Fatalf("stats %+v", s)
	}
}

func TestCursorCloseStopsWorker(t *testing.T) {
	c, plane := testCursor(t)
	plane.EXPECT().WaitVblank().Return(nil).Maybe()
	plane.EXPECT().Set(mock.Anything).Return(nil)
	c.start()
	c.Move(1, 2)
	c.close()
	c.close()    // idempotent
	c.Move(3, 4) // after close: no worker, must not block or panic
}

func TestCursorStats(t *testing.T) {
	c, plane := testCursor(t)
	applied := make(chan struct{}, 16)
	plane.EXPECT().WaitVblank().Return(nil)
	plane.EXPECT().Set(mock.Anything).RunAndReturn(func(v *modeCursor2) error {
		if v.flags == cursorMove {
			applied <- struct{}{}
		}
		return nil
	})
	c.start()
	defer c.close()
	c.Move(1, 1)
	<-applied
	c.Move(2, 2)
	<-applied
	s := c.TakeStats()
	if s.Moves != 2 || s.Ioctls != 2 {
		t.Fatalf("stats %+v", s)
	}
	if s := c.TakeStats(); s.Moves != 0 {
		t.Fatalf("not reset: %+v", s)
	}
}
