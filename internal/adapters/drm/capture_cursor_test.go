package drm

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestCaptureCursorSnapshot(t *testing.T) {
	var storage domain.CaptureCursor
	c := newCursor(nil, 64)
	c.image = true
	c.captureImage = domain.CursorImage{W: 1, H: 1, HotX: 2, Pixels: []byte{1, 2, 3, 255}}
	c.Move(10, 20)
	s := c.snapshot(&storage)
	if s == nil || s.X != 10 || s.Y != 20 || s.Image.HotX != 2 || s.Image.Pixels[2] != 3 {
		t.Fatalf("snapshot: %+v", s)
	}
	c.Hide()
	if c.snapshot(&storage) != nil {
		t.Fatal("away cursor captured")
	}
	c.Move(30, 40)
	c.image = false
	if c.snapshot(&storage) != nil {
		t.Fatal("security-invalidated cursor captured")
	}
}

func TestCaptureCursorIdleAllocations(t *testing.T) {
	c := newCursor(nil, 64)
	c.image = true
	c.captureImage = domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 0, 255, 255}}
	var storage domain.CaptureCursor
	noCursor := []domain.CaptureRequest{{Cursor: false}}
	for _, tc := range []struct {
		name             string
		normal, excluded []domain.CaptureRequest
	}{
		{name: "idle"}, {name: "cursor-free", normal: noCursor, excluded: noCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage.X = -123
			// Holding the motion lock proves that the idle path does not snapshot.
			c.mu.Lock()
			cycle := func() {
				if got := c.captureCursor(&storage, tc.normal, tc.excluded); got != nil {
					t.Fatal("unexpected cursor")
				}
			}
			allocs := testing.AllocsPerRun(100, cycle)
			c.mu.Unlock()
			if allocs != 0 || storage.X != -123 {
				t.Fatalf("allocs=%v storage=%+v", allocs, storage)
			}
			result := testing.Benchmark(func(b *testing.B) {
				for b.Loop() {
					cycle()
				}
			})
			t.Logf("%s %s", result.String(), result.MemString())
		})
	}
	for _, excluded := range []bool{false, true} {
		requests := []domain.CaptureRequest{{Cursor: true}}
		var normal, scoped []domain.CaptureRequest
		if excluded {
			scoped = requests
		} else {
			normal = requests
		}
		if got := c.captureCursor(&storage, normal, scoped); got != &storage || got.Image.W != 1 {
			t.Fatalf("owner snapshot: %+v", got)
		}
	}
}
