package drm

import (
	"sync"
	"testing"
	"time"
	"unsafe"
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

// The cursor moves once per vblank, to the latest position: moving it during
// scanout tears it into doubled images.
func TestCursorMovesOncePerVblank(t *testing.T) {
	vblank := make(chan struct{})
	moved := make(chan modeCursor2, 16)
	c := &Cursor{fd: -1, buf: &dumbBuffer{}, size: 64, shown: true}
	c.ioctl = func(v *modeCursor2) error { moved <- *v; return nil }
	c.vblank = func() error { <-vblank; return nil }
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
	if v := <-moved; v.x != 99 {
		t.Fatalf("applied x=%d, want latest 99", v.x)
	}
	select {
	case v := <-moved:
		t.Fatalf("extra move to %d without new input", v.x)
	case <-time.After(20 * time.Millisecond):
	}
}

// The legacy cursor ioctl may wait for vblank; Move must not, or libinput
// stops reading and the kernel drops mouse reports (slow, erratic pointer).
func TestCursorMoveDoesNotBlockOnIoctl(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var last modeCursor2
	c := &Cursor{fd: -1, buf: &dumbBuffer{}, size: 64, shown: true, hotX: 3, hotY: 4}
	c.ioctl = func(v *modeCursor2) error {
		time.Sleep(6 * time.Millisecond) // one 165 Hz vblank
		mu.Lock()
		defer mu.Unlock()
		calls++
		last = *v
		return nil
	}
	c.start()
	defer c.close()
	start := time.Now()
	for i := range 1000 { // one second of a 1 kHz mouse
		c.Move(float64(i), float64(2*i))
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("1000 moves took %v; Move blocks on the ioctl", d)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got, n := last, calls
		mu.Unlock()
		if got.x == 999-3 && got.y == 1998-4 {
			if got.flags != cursorMove || n >= 1000 {
				t.Fatalf("flags %#x after %d ioctls; want coalesced moves", got.flags, n)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("last applied %+v after %d ioctls; want the final position", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCursorCloseStopsWorker(t *testing.T) {
	c := &Cursor{buf: &dumbBuffer{}, shown: true, fd: -1}
	c.ioctl = func(*modeCursor2) error { return nil }
	c.start()
	c.Move(1, 2)
	c.close()
	c.close()    // idempotent
	c.Move(3, 4) // after close: no worker, must not block or panic
}

func TestCursorStats(t *testing.T) {
	applied := make(chan struct{}, 16)
	c := &Cursor{fd: -1, buf: &dumbBuffer{}, size: 64, shown: true}
	c.ioctl = func(*modeCursor2) error { applied <- struct{}{}; return nil }
	c.start()
	defer c.close()
	c.Move(1, 1)
	<-applied
	c.Move(2, 2)
	<-applied
	s := c.TakeStats()
	if s.Moves != 2 || s.Ioctls < 1 || s.Ioctls > 2 {
		t.Fatalf("stats %+v", s)
	}
	if s := c.TakeStats(); s.Moves != 0 {
		t.Fatalf("not reset: %+v", s)
	}
}
