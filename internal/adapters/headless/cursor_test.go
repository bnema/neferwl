package headless

import (
	"image"
	"image/color"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestCursorDraw(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for i := range dst.Pix {
		dst.Pix[i] = 255
	}
	// 2x2: opaque red, transparent, half black, opaque blue; hotspot (1,1).
	c := &Cursor{}
	c.set(domain.CursorImage{W: 2, H: 2, HotX: 1, HotY: 1, Pixels: []byte{0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 128, 255, 0, 0, 255}})
	c.Move(5, 5)
	c.draw(dst)
	for p, want := range map[image.Point]color.RGBA{{4, 4}: {255, 0, 0, 255}, {5, 4}: {255, 255, 255, 255}, {4, 5}: {127, 127, 127, 255}, {5, 5}: {0, 0, 255, 255}} {
		if got := dst.RGBAAt(p.X, p.Y); got != want {
			t.Errorf("%v = %v, want %v", p, got, want)
		}
	}
	// Off the edge: clipped, no panic.
	c.Move(0, 0)
	c.draw(dst)
	c.Move(100, -3)
	c.draw(dst)
}

func TestCaptureCursorSnapshotVisibility(t *testing.T) {
	var storage domain.CaptureCursor
	c := &Cursor{}
	if c.snapshot(&storage) != nil {
		t.Fatal("empty cursor visible")
	}
	c.set(domain.CursorImage{W: 1, H: 1, HotX: 1, Pixels: []byte{0, 0, 255, 255}})
	c.Move(12, 34)
	got := c.snapshot(&storage)
	if got == nil || got.X != 12 || got.Y != 34 || got.Image.HotX != 1 {
		t.Fatalf("snapshot: %+v", got)
	}
	c.Hide()
	if c.snapshot(&storage) != nil {
		t.Fatal("hidden cursor visible")
	}
	c.Move(20, 30)
	if c.snapshot(&storage) == nil {
		t.Fatal("motion did not show cursor")
	}
	c.set(domain.CursorImage{})
	if c.snapshot(&storage) != nil {
		t.Fatal("client-hidden cursor visible")
	}
}

func TestCaptureCursorIdleAllocations(t *testing.T) {
	c := &Cursor{}
	c.set(domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 0, 255, 255}})
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
