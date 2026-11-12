package vulkan

import (
	"bytes"
	"testing"
	"unsafe"
)

func TestSHMPendingCopyUsesSecondBufferAndRetiresIt(t *testing.T) {
	r := newTestRenderer(t, 2, 2)
	key := shmKey{win: 1}
	r.frame, r.inRender = 1, true
	red := bytes.Repeat([]byte{0, 0, 255, 255}, 4)
	green := bytes.Repeat([]byte{0, 255, 0, 255}, 4)
	a, err := r.shmCopyFor(key, shmState{w: 2, h: 2, seq: 1, windowSeq: 1}, red, nil, 0, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The current frame already reads a. Even with no busy fence it must not
	// be overwritten: polling may only complete earlier frames.
	b, err := r.shmCopyFor(key, shmState{w: 2, h: 2, seq: 2, windowSeq: 2}, green, nil, 0, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || r.shm[key].bufs[1] == nil {
		t.Fatal("pending reader overwritten instead of allocating second buffer")
	}
	if !bytes.Equal(unsafe.Slice((*byte)(a.gpu.mapped), 16), red) || !bytes.Equal(unsafe.Slice((*byte)(b.gpu.mapped), 16), green) {
		t.Fatal("copy pixels corrupted")
	}
	r.inRender = false
	r.frame = importTTL + 3
	r.completed = r.frame
	a.gpu.last = r.frame
	r.shm[key].last = r.frame
	r.dropShm()
	if r.shm[key].bufs[0] != a || r.shm[key].bufs[1] != nil || b.gpu.buffer != 0 {
		t.Fatal("unused second copy not retired")
	}
}
