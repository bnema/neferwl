package vulkan

import "testing"

func TestPollDoesNotCompleteRecordingFrame(t *testing.T) {
	c := &shmCopy{valid: true, holds: shmState{w: 2, h: 2, seq: 1}}
	key := shmKey{win: 1}
	r := &Renderer{frame: 3, submitted: 2, inRender: true, shm: map[shmKey]*shmSurface{key: {bufs: [2]*shmCopy{c}}}}
	if r.shmCached(key, c.holds) != c {
		t.Fatal("cached buffer missing")
	}
	if err := r.poll(); err != nil {
		t.Fatal(err)
	}
	if r.completed != 2 || c.gpu.last <= r.completed {
		t.Fatalf("recording frame treated as complete: last=%d completed=%d", c.gpu.last, r.completed)
	}
	r.inRender = false
	if err := r.poll(); err != nil {
		t.Fatal(err)
	}
	if r.completed != 3 {
		t.Fatalf("idle frame not completed: %d", r.completed)
	}
}
