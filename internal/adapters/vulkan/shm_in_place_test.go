package vulkan

import (
	"image/color"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
)

func TestSHMCompletedCopyInPlace(t *testing.T) {
	r := newTestRenderer(t, 32, 32)
	c := solidContent(t, 16, 16, color.RGBA{R: 255, A: 255})
	c.ID = 1
	scene := domain.Scene{Scale: 1, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 16, H: 16}}}}
	for i := range 60 {
		c.Seq = uint64(i + 1)
		c.Version = c.Seq
		px := make([]byte, 16*16*4)
		for j := 0; j < len(px); j += 4 {
			px[j+2], px[j+3] = uint8(i+1), 255
		}
		if _, err := c.SHM.File.WriteAt(px, 0); err != nil {
			t.Fatal(err)
		}
		if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: c}); err != nil {
			t.Fatal(err)
		}
		if got := readPixels(t, r).RGBAAt(8, 8); got != (color.RGBA{R: uint8(i + 1), A: 255}) {
			t.Fatalf("commit %d: %v", i, got)
		}
		for _, s := range r.shm {
			if s.bufs[0] == nil || s.bufs[1] != nil {
				t.Fatalf("commit %d did not keep exactly one copy", i)
			}
		}
	}
	t.Logf("60 completed commits: copied_bytes=%d buffers=1", r.CopiedBytes())
}

// Selection can be checked without a GPU: these are real CPU allocations used
// as already mapped storage, and all frames are already complete or unsubmitted.
func TestSHMCopySelectionAndDamage(t *testing.T) {
	for _, completed := range []uint64{0, 2} {
		a, b := make([]byte, 16), make([]byte, 16)
		first := &shmCopy{gpu: gpuBuffer{mapped: unsafe.Pointer(&a[0]), size: 16, last: 1}, holds: shmState{w: 2, h: 2, windowSeq: 1}, valid: true}
		second := &shmCopy{gpu: gpuBuffer{mapped: unsafe.Pointer(&b[0]), size: 16, last: 2}, holds: shmState{w: 2, h: 2, windowSeq: 2}, valid: true}
		key := shmKey{win: 1}
		r := &Renderer{gpu: &GPU{maxRange: 1024}, frame: 3, polledFrame: 3, completed: completed, shm: map[shmKey]*shmSurface{key: {bufs: [2]*shmCopy{first, second}}}}
		want := first
		if completed == 2 {
			want = second
		}
		got, err := r.shmCopyFor(key, shmState{w: 2, h: 2, seq: 3, windowSeq: 3}, make([]byte, 16), nil, 0, 8, func(since uint64) ([]domain.Rect, bool) {
			if since != want.holds.windowSeq {
				t.Fatalf("damage since %d, want %d", since, want.holds.windowSeq)
			}
			return []domain.Rect{{W: 1, H: 1}}, true
		})
		if err != nil || got != want {
			t.Fatalf("completed=%d: copy=%p want=%p err=%v", completed, got, want, err)
		}
		if r.copied != 4 {
			t.Fatalf("copied %d bytes, want 4", r.copied)
		}
	}
}
