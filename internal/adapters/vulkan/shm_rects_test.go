package vulkan

import (
	"image"
	"math/rand"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// disjointRects keeps the union of the damage, clipped to the buffer, and
// covers no pixel twice.
func TestDisjointRectsCoverUnionOnce(t *testing.T) {
	const w, h = 24, 16
	rng := rand.New(rand.NewSource(1))
	var scratch = disjointRects(nil, nil, w, h)
	for range 500 {
		rects := make([]ports.Rect, 1+rng.Intn(6))
		var want [w * h]bool
		for i := range rects {
			rects[i] = ports.Rect{X: rng.Intn(w+8) - 4, Y: rng.Intn(h+8) - 4, W: rng.Intn(14), H: rng.Intn(10)}
			for y := rects[i].Y; y < rects[i].Y+rects[i].H; y++ {
				for x := rects[i].X; x < rects[i].X+rects[i].W; x++ {
					if x >= 0 && x < w && y >= 0 && y < h {
						want[y*w+x] = true
					}
				}
			}
		}
		scratch = disjointRects(scratch[:0], rects, w, h)
		var got [w * h]int
		for _, r := range scratch {
			if r.Empty() {
				t.Fatalf("empty rect in %v for %v", scratch, rects)
			}
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					got[y*w+x]++
				}
			}
		}
		for i := range got {
			if (got[i] > 0) != want[i] || got[i] > 1 {
				t.Fatalf("pixel %d covered %d times (want %v) for %v -> %v", i, got[i], want[i], rects, scratch)
			}
		}
	}
}

// Client damage that would cut into too many pieces falls back to its
// bounding box: the work stays bounded and every damaged pixel is copied.
func TestDisjointRectsBoundsAdversarialDamage(t *testing.T) {
	const w, h = 1024, 1024
	rects := make([]ports.Rect, 128)
	for i := range rects {
		rects[i] = ports.Rect{X: i * 3, Y: i * 3, W: w - i*6, H: 4 + i}
	}
	got := disjointRects(nil, rects, w, h)
	if len(got) != 1 {
		t.Fatalf("%d pieces, want the bounding box", len(got))
	}
	for _, rc := range rects {
		r := image.Rect(rc.X, rc.Y, rc.X+rc.W, rc.Y+rc.H)
		if !r.In(got[0]) {
			t.Fatalf("damage %v outside %v", r, got[0])
		}
	}
}

// With a reused scratch slice the helper allocates nothing.
func TestDisjointRectsNoAllocs(t *testing.T) {
	rects := []ports.Rect{{X: 0, Y: 0, W: 10, H: 10}, {X: 5, Y: 5, W: 10, H: 10}, {X: 2, Y: 8, W: 4, H: 4}}
	scratch := disjointRects(nil, rects, 32, 32)
	if n := testing.AllocsPerRun(100, func() { scratch = disjointRects(scratch[:0], rects, 32, 32) }); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}
